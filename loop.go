// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/spin"
	"sync/atomic"
	"unsafe"
)

const pendingShards = 64

const cacheLineBytes = 64

type pendingShard[R any] struct {
	sl    spin.Lock
	items map[Token]*kont.Suspension[R]
	size  atomic.Int32
	_     [cacheLineBytes - unsafe.Sizeof(spin.Lock{}) - unsafe.Sizeof(map[Token]*kont.Suspension[R](nil)) - unsafe.Sizeof(atomic.Int32{})]byte
}

type shardedPending[R any] [pendingShards]pendingShard[R]

func newShardedPending[R any]() *shardedPending[R] {
	var sp shardedPending[R]
	for i := 0; i < pendingShards; i++ {
		sp[i] = pendingShard[R]{
			items: make(map[Token]*kont.Suspension[R]),
		}
	}
	return &sp
}

func (sp *shardedPending[R]) put(tok Token, susp *kont.Suspension[R]) {
	shard := &sp[tok%pendingShards]
	shard.sl.Lock()
	if _, ok := shard.items[tok]; !ok {
		shard.size.Add(1)
	}
	shard.items[tok] = susp
	shard.sl.Unlock()
}

func (sp *shardedPending[R]) get(tok Token) (*kont.Suspension[R], bool) {
	shard := &sp[tok%pendingShards]
	shard.sl.Lock()
	susp, ok := shard.items[tok]
	shard.sl.Unlock()
	return susp, ok
}

func (sp *shardedPending[R]) delete(tok Token) {
	shard := &sp[tok%pendingShards]
	shard.sl.Lock()
	if _, ok := shard.items[tok]; ok {
		delete(shard.items, tok)
		shard.sl.Unlock()
		shard.size.Add(-1)
		return
	}
	shard.sl.Unlock()
}

func (sp *shardedPending[R]) count() int {
	var n int
	for i := 0; i < pendingShards; i++ {
		n += int(sp[i].size.Load())
	}
	return n
}

// Loop drives Expr computations through a Backend.
type Loop[B Backend[B], R any] struct {
	backend     B
	pending     *shardedPending[R]
	completions []Completion
	results     []R
}

// NewLoop creates an event loop with the given backend and maximum completions polled per tick.
func NewLoop[B Backend[B], R any](b B, maxCompletions int) *Loop[B, R] {
	return &Loop[B, R]{
		backend:     b,
		pending:     newShardedPending[R](),
		completions: make([]Completion, maxCompletions),
		results:     make([]R, 0, maxCompletions),
	}
}

// SubmitExpr steps an Expr computation and submits its first operation.
func (l *Loop[B, R]) SubmitExpr(m kont.Expr[R]) (R, bool, error) {
	result, susp := kont.StepExpr(m)
	if susp == nil {
		return result, true, nil
	}
	tok, err := l.backend.Submit(susp.Op())
	if err != nil {
		susp.Discard()
		var zero R
		return zero, false, err
	}
	l.pending.put(tok, susp)
	var zero R
	return zero, false, nil
}

// Submit reifies a Cont computation and submits it.
func (l *Loop[B, R]) Submit(m kont.Eff[R]) (R, bool, error) {
	return l.SubmitExpr(kont.Reify(m))
}

// Poll dispatches ready completions. Resumed suspensions are resubmitted.
func (l *Loop[B, R]) Poll() ([]R, error) {
	n := l.backend.Poll(l.completions)
	l.results = l.results[:0]
	for i := 0; i < n; i++ {
		c := l.completions[i]
		susp, ok := l.pending.get(c.Token)
		if !ok {
			continue
		}

		if iox.IsMore(c.Err) {
			// Multishot: keep token alive.
			result, next := susp.Resume(c.Value)
			if next == nil {
				l.pending.delete(c.Token)
				l.results = append(l.results, result)
			}
			continue
		}
		// OK or failure: resume, delete token.
		l.pending.delete(c.Token)
		result, next := susp.Resume(c.Value)
		if next == nil {
			l.results = append(l.results, result)
		} else {
			tok, err := l.backend.Submit(next.Op())
			if err != nil {
				next.Discard()
				return l.results, err
			}
			l.pending.put(tok, next)
		}
	}
	return l.results, nil
}

// Pending returns the count of in-flight operations.
func (l *Loop[B, R]) Pending() int {
	return l.pending.count()
}

// Run polls until all pending operations complete.
func (l *Loop[B, R]) Run() ([]R, error) {
	var all []R
	var bo iox.Backoff
	for l.Pending() > 0 {
		results, err := l.Poll()
		if len(results) > 0 {
			all = append(all, results...)
		}
		if err != nil {
			return all, err
		}
		if len(results) > 0 {
			bo.Reset()
			continue
		}
		bo.Wait()
	}
	return all, nil
}
