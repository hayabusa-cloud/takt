// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"sync/atomic"
	"unsafe"

	"code.hybscloud.com/iobuf"
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/spin"
)

const (
	pendingShards  = 64
	cacheLineBytes = iobuf.CacheLineSize
)

type pendingShard[R any] struct {
	sl    spin.Lock
	items map[Token]*kont.Suspension[R]
	size  atomic.Int32
	_     [cacheLineBytes - unsafe.Sizeof(spin.Lock{}) - unsafe.Sizeof(map[Token]*kont.Suspension[R](nil)) - unsafe.Sizeof(atomic.Int32{})]byte
}

type shardedPending[R any] [pendingShards]pendingShard[R]

func newShardedPending[R any]() *shardedPending[R] {
	var sp shardedPending[R]
	for i := range pendingShards {
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

func (sp *shardedPending[R]) take(tok Token) (*kont.Suspension[R], bool) {
	shard := &sp[tok%pendingShards]
	shard.sl.Lock()
	susp, ok := shard.items[tok]
	if ok {
		delete(shard.items, tok)
		shard.sl.Unlock()
		shard.size.Add(-1)
		return susp, true
	}
	shard.sl.Unlock()
	return nil, false
}

func (sp *shardedPending[R]) count() int {
	var n int
	for i := range pendingShards {
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
// Returns [ErrUnsupportedMultishot] if a multishot completion would otherwise
// retarget a token to a new unsubmitted effect.
func (l *Loop[B, R]) Poll() ([]R, error) {
	results, err, _ := l.poll()
	return results, err
}

func (l *Loop[B, R]) poll() ([]R, error, bool) {
	var (
		n        int
		pollErr  error
		progress bool
	)
	n, pollErr = l.backend.Poll(l.completions)
	l.results = l.results[:0]
	for i := range n {
		c := l.completions[i]
		susp, ok := l.pending.take(c.Token)
		if !ok {
			continue
		}
		if iox.IsWouldBlock(c.Err) {
			tok, err := l.backend.Submit(susp.Op())
			if err != nil {
				// Backend ownership ended with the completion token; a failed re-arm
				// must explicitly discard the affine suspension before returning.
				susp.Discard()
				return l.results, err, progress
			}
			l.pending.put(tok, susp)
			continue
		}
		progress = true

		if iox.IsMore(c.Err) {
			// Multishot: keep token alive.
			result, next := susp.Resume(c.Value)
			if next == nil {
				l.results = append(l.results, result)
			} else {
				next.Discard()
				return l.results, ErrUnsupportedMultishot, true
			}
			continue
		}
		// OK or failure: resume, delete token.
		result, next := susp.Resume(c.Value)
		if next == nil {
			l.results = append(l.results, result)
		} else {
			tok, err := l.backend.Submit(next.Op())
			if err != nil {
				next.Discard()
				return l.results, err, progress
			}
			l.pending.put(tok, next)
		}
	}
	if iox.IsWouldBlock(pollErr) {
		pollErr = nil
	}
	return l.results, pollErr, progress
}

// Pending returns the count of in-flight operations.
func (l *Loop[B, R]) Pending() int {
	return l.pending.count()
}

// Run polls until all pending operations complete.
func (l *Loop[B, R]) Run() ([]R, error) {
	pending := l.Pending()
	if pending == 0 {
		return nil, nil
	}
	all := make([]R, 0, pending)
	var bo iox.Backoff
	for pending > 0 {
		results, err, progress := l.poll()
		if len(results) > 0 {
			all = append(all, results...)
		}
		if err != nil {
			return all, err
		}
		if progress {
			bo.Reset()
		} else {
			bo.Wait()
		}
		pending = l.Pending()
	}
	return all, nil
}
