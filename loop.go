// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

type pending[R any] struct {
	susp     *kont.Suspension[R]
	reply    int // batch position; consumed before the completion slab is reused
	deferred bool
}

// Loop owns a bounded set of affine computation lineages. Admission reserves
// local capacity; Poll offers queued operations to the backend. A successor
// reuses its predecessor's record. Native requests and backing storage remain
// the backend binding's responsibility, including after Drain or failure.
//
// All access to one Loop must be serialized. State-changing calls must also be
// non-reentrant, including callbacks reached during evaluation or resumption.
// A Loop must not be copied after construction. Panics through callbacks are
// outside the runner's abort contract. Replies must remain valid through batch
// accounting and failure; discarded values require an admitted disposition.
type Loop[B Backend[B], R any] struct {
	backend            B
	memory             CompletionMemory
	pending            table[Token, pending[R]]
	admission, replies queue
	fresh              int
	completions        []Completion
	results            []R
	fatal              error
}

// NewLoop provisions capacity computation records. Capacity must be positive.
// [WithMaxCompletions] independently bounds offers, observations and resumptions
// per Poll. [WithMemory] selects the cold completion-slab provider. Construction
// provisions all bookkeeping; arbitrary programs and backends can still allocate.
func NewLoop[B Backend[B], R any](b B, capacity int, opts ...Option) *Loop[B, R] {
	if capacity <= 0 {
		panic("takt: capacity must be positive")
	}
	cfg := resolveLoopConfig(opts)
	completions := newCompletionBuf(cfg.memory, cfg.maxCompletions)
	return &Loop[B, R]{
		backend:     b,
		memory:      cfg.memory,
		pending:     newTable[Token, pending[R]](capacity),
		admission:   newQueue(capacity),
		replies:     newQueue(capacity),
		completions: completions,
		results:     make([]R, 0, len(completions)),
	}
}

func newCompletionBuf(mem CompletionMemory, maxCompletions int) []Completion {
	if maxCompletions > 0 {
		buf := mem.CompletionBuf(WithSize(maxCompletions))
		if len(buf) < maxCompletions {
			panic("takt: CompletionMemory returned short completion buffer")
		}
		return buf[:maxCompletions]
	}
	buf := mem.CompletionBuf()
	if len(buf) == 0 {
		panic("takt: CompletionMemory returned empty default completion buffer")
	}
	return buf
}

// SubmitExpr reserves before evaluating m. Local capacity refusal returns
// iox.ErrWouldBlock without executing a program prefix. A suspended result is
// queued locally; backend acceptance occurs in a later Poll.
func (l *Loop[B, R]) SubmitExpr(m kont.Expr[R]) (R, bool, error) {
	var zero R
	if l.fatal != nil {
		return zero, false, l.fatal
	}
	i := l.pending.reserve()
	if i == -1 {
		return zero, false, iox.ErrWouldBlock
	}
	return l.start(i, m)
}

func (l *Loop[B, R]) start(i int, m kont.Expr[R]) (R, bool, error) {
	result, susp := kont.StepExpr(m)
	if susp == nil {
		l.pending.release(i)
		return result, true, nil
	}
	l.enqueue(i, susp, false)
	var zero R
	return zero, false, nil
}

// Submit reserves capacity before reifying m, so refusal executes no prefix.
func (l *Loop[B, R]) Submit(m kont.Eff[R]) (R, bool, error) {
	var zero R
	if l.fatal != nil {
		return zero, false, l.fatal
	}
	i := l.pending.reserve()
	if i == -1 {
		return zero, false, iox.ErrWouldBlock
	}
	return l.start(i, kont.Reify(m))
}

// SubmitSuspension transfers a reached suspension on nil. Any returned error
// leaves it unconsumed and owned by the caller. susp must be non-nil and unused.
func (l *Loop[B, R]) SubmitSuspension(susp *kont.Suspension[R]) error {
	if l.fatal != nil {
		return l.fatal
	}
	i := l.pending.reserve()
	if i == -1 {
		return iox.ErrWouldBlock
	}
	l.enqueue(i, susp, false)
	return nil
}
func (l *Loop[B, R]) enqueue(i int, susp *kont.Suspension[R], deferred bool) {
	l.pending.entries[i].value = pending[R]{susp: susp, deferred: deferred}
	l.admission.push(i)
	if !deferred {
		l.fresh++
	}
}

// Ready reports fresh admissions or retained replies. Deferred retries remain
// Pending without forcing Ready. It does not promise backend acceptance.
// External drivers supply readiness wakes or retry deadlines before parking.
func (l *Loop[B, R]) Ready() bool {
	return l.fatal == nil && (l.fresh > 0 || l.replies.size > 0)
}

// Poll runs one bounded driver round: offer a FIFO snapshot, service the backend,
// account the whole batch, then resume replies in observation order. Successors
// are offered in later rounds. Each explicit call permits bounded deferred retries.
// WouldBlock preserves the reached suspension; completion-level WouldBlock also
// requires that the old request retired with no reply or hidden partial progress.
// More is unsupported by this one-shot runner.
//
// Replies before a fatal observation may resume under the binding's stable-reply
// and abort premises; their successors are discarded. The scalar abort profile
// disposes the remaining batch. Resource-producing bindings must separately own
// every undelivered result. Results already completed precede the stored error.
// The returned slice is borrowed until the next Poll, Run or Drain. Copy values
// that must survive that boundary; the runner clears its expired references.
func (l *Loop[B, R]) Poll() ([]R, error) {
	results, err, _ := l.step()
	return results, err
}

func (l *Loop[B, R]) step() ([]R, error, bool) {
	clear(l.results)
	l.results = l.results[:0]
	if l.fatal != nil {
		return nil, l.fatal, false
	}
	progress := false
	for range min(l.admission.size, len(l.completions)) {
		i := l.admission.pop()
		p := &l.pending.entries[i].value
		if !p.deferred {
			l.fresh--
		}
		token, err := l.backend.Submit(p.susp.Op())
		if iox.IsWouldBlock(err) {
			p.deferred = true
			l.admission.push(i)
			continue
		}
		if err != nil {
			l.poison(err)
			return nil, err, progress
		}
		if !l.pending.insert(i, token, uint64(token)) {
			l.poison(ErrLiveTokenReuse)
			return nil, l.fatal, progress
		}
	}
	n, err := l.backend.Poll(l.completions)
	if n > 0 {
		defer clearCompletions(l.completions[:n])
	}
	if err != nil && !iox.IsWouldBlock(err) {
		l.poison(err)
		return nil, err, progress
	}
	for j := range n {
		c := &l.completions[j]
		// Every fetched entry has the binding's disposal, including a fatal suffix.
		if l.fatal != nil {
			continue
		}
		i := l.pending.remove(c.Token, uint64(c.Token))
		if i == -1 {
			continue
		}
		p := &l.pending.entries[i].value
		switch iox.Classify(c.Err) {
		case iox.OutcomeWouldBlock:
			l.enqueue(i, p.susp, true)
		case iox.OutcomeMore:
			l.fatal = ErrUnsupportedMultishot
		default:
			p.reply = j
			l.replies.push(i)
		}
	}
	for range min(l.replies.size, len(l.completions)) {
		i := l.replies.pop()
		p := &l.pending.entries[i].value
		result, next := p.susp.Resume(l.completions[p.reply].ResumeValue())
		p.susp = nil
		p.reply = 0
		progress = true
		if next == nil {
			l.results = append(l.results, result)
			l.pending.release(i)
		} else if l.fatal != nil {
			next.Discard()
			l.pending.release(i)
		} else {
			l.enqueue(i, next, false)
		}
	}
	if l.fatal != nil {
		l.discard()
		if len(l.results) == 0 {
			return nil, l.fatal, progress
		}
	}
	if len(l.results) == 0 && !progress {
		return nil, nil, false
	}
	return l.results, nil, progress
}

func (l *Loop[B, R]) poison(err error) {
	if l.fatal == nil {
		l.fatal = err
	}
	l.discard()
}

func (l *Loop[B, R]) discard() int {
	n := l.pending.count
	for i := range l.pending.entries {
		if l.pending.entries[i].used {
			if s := l.pending.entries[i].value.susp; s != nil {
				s.Discard()
			}
			l.pending.release(i)
		}
	}
	for i := range l.pending.buckets {
		l.pending.buckets[i] = -1
	}
	l.admission.size = 0
	l.replies.size = 0
	l.fresh = 0
	return n
}

// Pending counts local lineages, including queued and deferred work.
// Zero does not establish native settlement or safe backing-storage release.
func (l *Loop[B, R]) Pending() int { return l.pending.count }

// Failed returns the first terminal runner error, preserved by later calls.
func (l *Loop[B, R]) Failed() error { return l.fatal }

// Drain abandons every owned capture once under the binding's abort contract.
// It releases the completion slab once and costs O(capacity). The native owner
// must continue servicing outstanding requests and resource uses.
func (l *Loop[B, R]) Drain() int {
	if l.fatal == nil {
		l.fatal = ErrDisposed
	}
	n := l.discard()
	if l.completions != nil {
		l.memory.Release(l.completions)
		l.completions = nil
	}
	clear(l.results)
	l.results = l.results[:0]
	return n
}

// Run collects pending results into a caller-owned allocating slice. It uses
// adaptive backoff as a retry timer. Fresh local work prevents parking; mere
// refusal or rearming does not reset backoff. It is not a native cleanup driver.
func (l *Loop[B, R]) Run() ([]R, error) {
	clear(l.results)
	l.results = l.results[:0]
	if l.fatal != nil {
		return nil, l.fatal
	}
	if l.Pending() == 0 {
		return nil, nil
	}
	all := make([]R, 0, l.Pending())
	var bo iox.Backoff
	for l.Pending() > 0 {
		results, err, progress := l.step()
		all = append(all, results...)
		if err != nil {
			return all, err
		}
		if l.fatal != nil {
			return all, l.fatal
		}
		if progress {
			bo.Reset()
		}
		if l.Pending() > 0 && !l.Ready() && !progress {
			bo.Wait()
		}
	}
	return all, nil
}
