// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"sync/atomic"
	"unsafe"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/spin"
)

type routeState struct {
	canceling bool
}

const routeShards = 64

type routeShard struct {
	sl    spin.Lock
	items map[RouteID]routeState
	size  atomic.Int32
	_     [cacheLineBytes - unsafe.Sizeof(spin.Lock{}) - unsafe.Sizeof(map[RouteID]routeState(nil)) - unsafe.Sizeof(atomic.Int32{})]byte
}

// Compile-time check: routeShard occupies exactly one cache line. The route
// table mirrors Loop's token table so hot route lookup, insert, and delete
// traffic stays shard-local.
var _ [0]byte = [cacheLineBytes - unsafe.Sizeof(routeShard{})]byte{}

type shardedRoutes [routeShards]routeShard

func newShardedRoutes() *shardedRoutes {
	var routes shardedRoutes
	for i := range routeShards {
		routes[i] = routeShard{
			items: make(map[RouteID]routeState),
		}
	}
	return &routes
}

func routeShardIndex(id RouteID) int {
	return int((uint64(id.token) ^ id.generation) & (routeShards - 1))
}

func (routes *shardedRoutes) store(id RouteID, st routeState) bool {
	shard := &routes[routeShardIndex(id)]
	shard.sl.Lock()
	if _, ok := shard.items[id]; ok {
		shard.sl.Unlock()
		return false
	}
	shard.size.Add(1)
	shard.items[id] = st
	shard.sl.Unlock()
	return true
}

func (routes *shardedRoutes) load(id RouteID) (routeState, bool) {
	shard := &routes[routeShardIndex(id)]
	shard.sl.Lock()
	st, ok := shard.items[id]
	shard.sl.Unlock()
	return st, ok
}

func (routes *shardedRoutes) setCanceling(id RouteID) bool {
	shard := &routes[routeShardIndex(id)]
	shard.sl.Lock()
	st, ok := shard.items[id]
	if ok {
		st.canceling = true
		shard.items[id] = st
	}
	shard.sl.Unlock()
	return ok
}

func (routes *shardedRoutes) delete(id RouteID) {
	shard := &routes[routeShardIndex(id)]
	shard.sl.Lock()
	delete(shard.items, id)
	shard.sl.Unlock()
	shard.size.Add(-1)
}

func (shard *routeShard) claimAny() (RouteID, routeState, bool) {
	shard.sl.Lock()
	for id, st := range shard.items {
		delete(shard.items, id)
		shard.sl.Unlock()
		shard.size.Add(-1)
		return id, st, true
	}
	shard.sl.Unlock()
	return RouteID{}, routeState{}, false
}

func (routes *shardedRoutes) size() int {
	var n int
	for i := range routeShards {
		n += int(routes[i].size.Load())
	}
	return n
}

func (routes *shardedRoutes) drain(cancel func(RouteID) error) int {
	drained := 0
	for i := range routeShards {
		shard := &routes[i]
		for {
			id, st, ok := shard.claimAny()
			if !ok {
				break
			}
			if !st.canceling {
				_ = cancel(id)
			}
			drained++
		}
	}
	return drained
}

// SubscriptionLoop drives route-indexed stream completions through a
// [SubscriptionBackend].
//
// SubscriptionLoop is separate from [Loop]: Loop owns one-shot suspended
// computations, while SubscriptionLoop owns same-route successor observations.
// A [StreamCompletion] with More set emits a non-final [StreamEvent] and keeps
// route ownership live; a completion without More emits a final event and
// retires the route.
//
// SubscriptionLoop is not safe for concurrent use. Callers must serialize
// Subscribe, Poll, Cancel, Drain, Pending, and Failed calls on one loop.
type SubscriptionLoop[B SubscriptionBackend[B, A], A any] struct {
	backend     B
	routes      *shardedRoutes
	completions []StreamCompletion[A]
	events      []StreamEvent[A]
	fatal       error
}

// NewSubscriptionLoop creates a route-indexed stream runner over b.
// Use [WithMaxStreamCompletions] to cap the per-poll completion buffer length;
// omitted options use [DefaultCompletionBufSize].
func NewSubscriptionLoop[B SubscriptionBackend[B, A], A any](b B, opts ...SubscriptionOption) *SubscriptionLoop[B, A] {
	cfg := resolveSubscriptionConfig(opts)
	completions := make([]StreamCompletion[A], cfg.maxCompletions)
	return &SubscriptionLoop[B, A]{
		backend:     b,
		routes:      newShardedRoutes(),
		completions: completions,
		events:      make([]StreamEvent[A], 0, len(completions)),
	}
}

// Subscribe starts op through the backend and stores the returned route.
// It returns [ErrInvalidRouteID] when the backend returns the reserved zero
// route and [ErrLiveRouteReuse] when the backend aliases a currently live
// route. Either condition is fatal to the loop.
func (l *SubscriptionLoop[B, A]) Subscribe(op kont.Operation) (Subscription[A], error) {
	if l.fatal != nil {
		return Subscription[A]{}, l.fatal
	}
	id, err := l.backend.Subscribe(op)
	if err != nil {
		return Subscription[A]{}, err
	}
	if id.IsZero() {
		l.poison(ErrInvalidRouteID)
		return Subscription[A]{}, ErrInvalidRouteID
	}
	if !l.routes.store(id, routeState{}) {
		l.poison(ErrLiveRouteReuse)
		return Subscription[A]{}, ErrLiveRouteReuse
	}
	return Subscription[A]{id: id}, nil
}

// Poll emits ready stream events.
//
// Poll-level [iox.ErrWouldBlock] is treated as an idle tick. The backend poll
// contract excludes ready completions paired with a non-nil poll error.
// Unknown-route completions are stale observations and are ignored. The returned
// slice aliases an internal buffer that is reused on the next Poll call.
func (l *SubscriptionLoop[B, A]) Poll() ([]StreamEvent[A], error) {
	if l.fatal != nil {
		return nil, l.fatal
	}
	n, err := l.backend.Poll(l.completions)
	l.events = l.events[:0]
	if n > 0 {
		defer clearStreamCompletions(l.completions[:n])
	}
	if err != nil {
		if iox.IsWouldBlock(err) {
			return nil, nil
		}
		l.poison(err)
		return nil, err
	}
	for i := range n {
		l.handleCompletion(l.completions[i])
	}
	if len(l.events) == 0 {
		return nil, nil
	}
	return l.events, nil
}

func (l *SubscriptionLoop[B, A]) handleCompletion(c StreamCompletion[A]) {
	if c.ID.IsZero() {
		return
	}
	if _, ok := l.routes.load(c.ID); !ok {
		return
	}
	ev := StreamEvent[A]{
		Subscription: Subscription[A]{id: c.ID},
		Value:        c.Value,
		HasValue:     c.HasValue,
		Final:        !c.More,
		EventErr:     c.EventErr,
	}
	l.events = append(l.events, ev)
	if !c.More {
		l.routes.delete(c.ID)
	}
}

// Cancel requests cancellation of sub.
// A successful cancel request preserves the route until a terminal completion,
// Drain, or fatal loop transition retires it. Cancel returns
// [ErrUnknownSubscription] for a zero or non-live subscription handle.
func (l *SubscriptionLoop[B, A]) Cancel(sub Subscription[A]) error {
	if l.fatal != nil {
		return l.fatal
	}
	if sub.IsZero() {
		return ErrUnknownSubscription
	}
	st, ok := l.routes.load(sub.id)
	if !ok {
		return ErrUnknownSubscription
	}
	if st.canceling {
		return nil
	}
	if err := l.backend.Cancel(sub.id); err != nil {
		return err
	}
	l.routes.setCanceling(sub.id)
	return nil
}

// Pending returns the count of live stream routes.
func (l *SubscriptionLoop[B, A]) Pending() int {
	return l.routes.size()
}

// Failed returns the loop's terminal fatal error, if any.
func (l *SubscriptionLoop[B, A]) Failed() error {
	return l.fatal
}

// Drain transitions the stream loop into a disposed state and retires every
// owned route. Active routes receive one cancel request; routes already in the
// canceling state are retired without a duplicate cancel request. Drain is
// idempotent and returns the number of routes retired by this call.
func (l *SubscriptionLoop[B, A]) Drain() int {
	if l.fatal == nil {
		l.fatal = ErrDisposed
	}
	drained := l.drainRoutes()
	if l.completions != nil {
		clearStreamCompletions(l.completions)
		l.completions = nil
	}
	l.events = nil
	return drained
}

func (l *SubscriptionLoop[B, A]) poison(err error) {
	if l.fatal == nil {
		l.fatal = err
	}
	l.drainRoutes()
}

func (l *SubscriptionLoop[B, A]) drainRoutes() int {
	return l.routes.drain(l.backend.Cancel)
}

func clearStreamCompletions[A any](s []StreamCompletion[A]) {
	clear(s)
}
