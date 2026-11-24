// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"math/bits"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

type routeState struct {
	canceling bool
}

// Rotating the generation spreads routes whose token and generation advance together.
func routeHash(id RouteID) uint64 {
	return uint64(id.token) ^ bits.RotateLeft64(id.generation, 32)
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
// all access to one loop; backend callbacks must not reenter state-changing
// methods. A SubscriptionLoop must not be copied after construction.
type SubscriptionLoop[B SubscriptionBackend[B, A], A any] struct {
	backend     B
	routes      table[RouteID, routeState]
	completions []StreamCompletion[A]
	events      []StreamEvent[A]
	fatal       error
}

// NewSubscriptionLoop reserves space for capacity simultaneous routes.
// Capacity must be positive.
// Use [WithMaxStreamCompletions] to cap the per-poll completion buffer length;
// omitted options use [DefaultCompletionBufSize].
func NewSubscriptionLoop[B SubscriptionBackend[B, A], A any](b B, capacity int, opts ...SubscriptionOption) *SubscriptionLoop[B, A] {
	if capacity <= 0 {
		panic("takt: capacity must be positive")
	}
	cfg := resolveSubscriptionConfig(opts)
	completions := make([]StreamCompletion[A], cfg.maxCompletions)
	return &SubscriptionLoop[B, A]{
		backend:     b,
		routes:      newTable[RouteID, routeState](capacity),
		completions: completions,
		events:      make([]StreamEvent[A], 0, len(completions)),
	}
}

// Subscribe reserves a route record before calling the backend. Local capacity
// exhaustion returns [iox.ErrWouldBlock] before the backend sees op. Success
// means the backend accepted the route.
// It returns [ErrInvalidRouteID] when the backend returns the reserved zero
// route and [ErrLiveRouteReuse] when the backend aliases a currently live
// route. Either condition is fatal to the loop.
func (l *SubscriptionLoop[B, A]) Subscribe(op kont.Operation) (Subscription[A], error) {
	if l.fatal != nil {
		return Subscription[A]{}, l.fatal
	}
	// The serialized owner keeps this slot available until the offer returns.
	slot := l.routes.free
	if slot == -1 {
		return Subscription[A]{}, iox.ErrWouldBlock
	}
	id, err := l.backend.Subscribe(op)
	if err != nil {
		return Subscription[A]{}, err
	}
	if id.IsZero() {
		l.poison(ErrInvalidRouteID)
		return Subscription[A]{}, ErrInvalidRouteID
	}
	l.routes.takeFree(slot)
	if !l.routes.insert(slot, id, routeHash(id)) {
		l.routes.release(slot)
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
// slice is borrowed until the next Poll or Drain. Copy events that must survive
// that boundary; the runner clears its expired value and error references.
func (l *SubscriptionLoop[B, A]) Poll() ([]StreamEvent[A], error) {
	clear(l.events)
	l.events = l.events[:0]
	if l.fatal != nil {
		return nil, l.fatal
	}
	n, err := l.backend.Poll(l.completions)
	if n > 0 {
		defer clear(l.completions[:n])
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
	if c.More {
		if l.routes.lookup(c.ID, routeHash(c.ID)) == -1 {
			return
		}
	} else {
		i := l.routes.remove(c.ID, routeHash(c.ID))
		if i == -1 {
			return
		}
		l.routes.release(i)
	}
	ev := StreamEvent[A]{
		Subscription: Subscription[A]{id: c.ID},
		Value:        c.Value,
		HasValue:     c.HasValue,
		Final:        !c.More,
		EventErr:     c.EventErr,
	}
	l.events = append(l.events, ev)
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
	i := l.routes.lookup(sub.id, routeHash(sub.id))
	if i == -1 {
		return ErrUnknownSubscription
	}
	if l.routes.entries[i].value.canceling {
		return nil
	}
	if err := l.backend.Cancel(sub.id); err != nil {
		return err
	}
	l.routes.entries[i].value.canceling = true
	return nil
}

// Pending returns the count of live stream routes.
func (l *SubscriptionLoop[B, A]) Pending() int {
	return l.routes.count
}

// Failed returns the loop's terminal fatal error, if any.
func (l *SubscriptionLoop[B, A]) Failed() error {
	return l.fatal
}

// Drain transitions the stream loop into a disposed state and retires every
// owned route. Active routes receive one cancel request; routes already in the
// canceling state are retired without a duplicate cancel request. Drain is
// idempotent and returns the number of routes retired by this call.
// The binding's native owner must retain cancellation refusals and dispose late
// resource-bearing events. Local retirement is not physical settlement.
func (l *SubscriptionLoop[B, A]) Drain() int {
	if l.fatal == nil {
		l.fatal = ErrDisposed
	}
	drained := l.drainRoutes()
	clear(l.completions)
	l.completions = nil
	clear(l.events)
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
	n := l.routes.count
	for i := range l.routes.entries {
		e := &l.routes.entries[i]
		if e.used {
			id, canceling := e.key, e.value.canceling
			l.routes.release(i)
			if !canceling {
				_ = l.backend.Cancel(id)
			}
		}
	}
	for i := range l.routes.buckets {
		l.routes.buckets[i] = -1
	}
	return n
}
