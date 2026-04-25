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

// cacheLineBytes mirrors iobuf.CacheLineSize for use in shared cache-line
// padding constants across this package.
const cacheLineBytes = iobuf.CacheLineSize

func classifyCompletion(c Completion) iox.Outcome {
	return iox.Classify(c.Err)
}

type resumeResult[R any] struct {
	done   bool
	result R
}

const (
	pendingShards = 64
)

type pendingShard[R any] struct {
	sl    spin.Lock
	items map[Token]*kont.Suspension[R]
	size  atomic.Int32
	_     [cacheLineBytes - unsafe.Sizeof(spin.Lock{}) - unsafe.Sizeof(map[Token]*kont.Suspension[R](nil)) - unsafe.Sizeof(atomic.Int32{})]byte
}

// Compile-time check: pendingShard occupies exactly one cache line. Future field
// growth that would overflow the padding fails the build instead of silently
// breaking the false-sharing guarantee. The shard is independent of R for size
// purposes (map header and atomic.Int32 do not vary).
var _ [0]byte = [cacheLineBytes - unsafe.Sizeof(pendingShard[struct{}]{})]byte{}

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

func (sp *shardedPending[R]) store(tok Token, susp *kont.Suspension[R]) bool {
	shard := &sp[tok%pendingShards]
	shard.sl.Lock()
	if _, ok := shard.items[tok]; ok {
		shard.sl.Unlock()
		return false
	}
	shard.size.Add(1)
	shard.items[tok] = susp
	shard.sl.Unlock()
	return true
}

func (sp *shardedPending[R]) claim(tok Token) (*kont.Suspension[R], bool) {
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

func (sp *shardedPending[R]) size() int {
	var n int
	for i := range pendingShards {
		n += int(sp[i].size.Load())
	}
	return n
}

// drainAll discards every stored suspension and returns the number drained.
// It is used when the loop transitions to a fatal state so no
// `*kont.Suspension[R]` is left pending.
func (sp *shardedPending[R]) drainAll() int {
	var drained int
	for i := range pendingShards {
		shard := &sp[i]
		shard.sl.Lock()
		for tok, susp := range shard.items {
			susp.Discard()
			delete(shard.items, tok)
			drained++
		}
		shard.size.Store(0)
		shard.sl.Unlock()
	}
	return drained
}

// Loop drives Expr computations through a Backend.
// It owns pending `*kont.Suspension` frontiers keyed by [Token], so one live
// token refers to at most one pending suspension at a time.
type Loop[B Backend[B], R any] struct {
	backend     B
	memory      CompletionMemory
	pending     *shardedPending[R]
	completions []Completion
	results     []R
	fatal       error
}

// NewLoop creates an event loop with the given backend. Behavior is configured
// via functional [Option]s:
//
//   - [WithMemory] installs a custom [CompletionMemory] provider; the default is a
//     fresh [HeapMemory] (sync.Pool-backed default-sized slabs).
//   - [WithMaxCompletions] caps the completion slab length per poll;
//     omitting it lets the CompletionMemory provider choose (typically
//     [DefaultCompletionBufSize]).
//
// Use [BoundedMemory] via [WithMemory] when completion buffers should come from
// a single bounded pool of default-sized 128 KiB slabs. Custom providers may
// implement [CompletionMemory] for any allocation strategy without widening the
// [Backend] or [Completion] contracts.
func NewLoop[B Backend[B], R any](b B, opts ...Option) *Loop[B, R] {
	cfg := resolveLoopConfig(opts)
	completions := newCompletionBuf(cfg.memory, cfg.maxCompletions)
	return &Loop[B, R]{
		backend:     b,
		memory:      cfg.memory,
		pending:     newShardedPending[R](),
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

// SubmitExpr steps a [kont.Expr] computation and transfers its first
// suspension to the backend.
// The loop stores the resulting frontier under one token, so later completions
// are expected to continue that same affine suspension lineage.
func (l *Loop[B, R]) SubmitExpr(m kont.Expr[R]) (R, bool, error) {
	if l.fatal != nil {
		var zero R
		return zero, false, l.fatal
	}
	result, susp := kont.StepExpr(m)
	if susp == nil {
		return result, true, nil
	}
	if err := l.submit(susp); err != nil {
		var zero R
		return zero, false, err
	}
	var zero R
	return zero, false, nil
}

// Submit reifies a [kont.Eff] computation and submits it.
func (l *Loop[B, R]) Submit(m kont.Eff[R]) (R, bool, error) {
	return l.SubmitExpr(kont.Reify(m))
}

func (l *Loop[B, R]) submit(susp *kont.Suspension[R]) error {
	tok, err := l.backend.Submit(susp.Op())
	if err != nil {
		susp.Discard()
		return err
	}
	if !l.pending.store(tok, susp) {
		susp.Discard()
		l.poison(ErrLiveTokenReuse)
		return ErrLiveTokenReuse
	}
	return nil
}

// Poll dispatches ready completions. A resumed suspension either completes or
// is submitted again under a fresh token. The only unsupported case is a
// multishot completion (`iox.ErrMore`) that resumes into a new suspended
// operation; Poll reports [ErrUnsupportedMultishot] for that case because the
// loop otherwise could not preserve its token-to-suspension correlation.
//
// Returns completed results when one or more computations finish in the current
// poll tick, a zero-length non-nil slice when work advanced without producing a
// completed computation, (nil, nil) when idle, or (nil, err) on infrastructure
// failure. Poll never returns both results and a non-nil error; if a fatal
// failure arrives in the same tick as ready results, the failure is reported on
// the next call.
//
// The returned []R aliases an internal buffer that is reused across Poll/Run
// calls; callers that need to retain the slice past the next Poll/Run must
// copy it.
//
// When the loop enters a fatal state, whether from an infrastructure poll
// failure or a completion-driven failure such as [ErrUnsupportedMultishot] or
// [ErrLiveTokenReuse], it discards every pending suspension exactly once before
// returning.
func (l *Loop[B, R]) Poll() ([]R, error) {
	results, err, _ := l.step()
	return results, err
}

func (l *Loop[B, R]) step() ([]R, error, bool) {
	if l.fatal != nil {
		return nil, l.fatal, false
	}

	var (
		n        int
		pollErr  error
		progress bool
	)
	n, pollErr = l.backend.Poll(l.completions)
	l.results = l.results[:0]
	if n > 0 {
		defer clearCompletions(l.completions[:n])
	}

	if pollErr != nil {
		if iox.IsWouldBlock(pollErr) {
			pollErr = nil
		} else {
			l.poison(pollErr)
			return nil, pollErr, false
		}
	}

	for i := range n {
		ready, advanced, err := l.handleCompletion(l.completions[i])
		if advanced {
			progress = true
		}
		if err != nil {
			l.poison(err)
			if len(l.results) > 0 {
				return l.results, nil, progress
			}
			return nil, err, progress
		}
		if ready.done {
			l.results = append(l.results, ready.result)
		}
	}
	if len(l.results) == 0 && !progress {
		return nil, nil, false
	}
	return l.results, nil, progress
}

// poison transitions the loop into the terminal fatal state and drains every
// pending suspension. Idempotent: a second call preserves the original error.
func (l *Loop[B, R]) poison(err error) {
	if l.fatal == nil {
		l.fatal = err
	}
	l.pending.drainAll()
}

func (l *Loop[B, R]) handleCompletion(c Completion) (resumeResult[R], bool, error) {
	susp, ok := l.pending.claim(c.Token)
	if !ok {
		return resumeResult[R]{}, false, nil
	}

	outcome := classifyCompletion(c)
	if outcome == iox.OutcomeWouldBlock {
		// Backend ownership ended with the completion token; a failed re-arm
		// must discard the suspension before returning. WouldBlock remains a
		// no-progress observation even though the suspension is re-armed.
		return resumeResult[R]{}, false, l.submit(susp)
	}

	result, next := susp.Resume(c.Value)

	// OutcomeWouldBlock is handled above; only OK, More, and Failure remain.
	// OutcomeOK and OutcomeFailure follow the same resume path because the
	// backend already encodes the operation result in Completion.Value.
	if outcome == iox.OutcomeMore {
		// The same backend submission remains live. This is only supported
		// when resuming does not produce a fresh suspended operation.
		if next == nil {
			return resumeResult[R]{done: true, result: result}, true, nil
		}
		next.Discard()
		return resumeResult[R]{}, true, ErrUnsupportedMultishot
	}
	if next == nil {
		return resumeResult[R]{done: true, result: result}, true, nil
	}
	return resumeResult[R]{}, true, l.submit(next)
}

// Pending returns the count of in-flight operations.
func (l *Loop[B, R]) Pending() int {
	return l.pending.size()
}

// Failed returns the loop's terminal fatal error, if any. A non-nil result
// means every subsequent SubmitExpr, Submit, or Poll call returns the same
// error and all previously pending suspensions have been discarded exactly once.
func (l *Loop[B, R]) Failed() error {
	return l.fatal
}

// Drain transitions the loop into a terminal disposed state and discards every
// pending suspension exactly once. Drain is idempotent and safe to call after a
// fatal failure has already been recorded; it preserves the existing fatal
// error if one is set, otherwise it records [ErrDisposed] so subsequent
// SubmitExpr, Submit, and Poll calls fail fast. Drain also returns the
// completion slab to its [CompletionMemory] provider exactly once. It returns
// the number of suspensions drained by this call.
func (l *Loop[B, R]) Drain() int {
	if l.fatal == nil {
		l.fatal = ErrDisposed
	}
	drained := l.pending.drainAll()
	if l.completions != nil {
		l.memory.Release(l.completions)
		l.completions = nil
	}
	return drained
}

// Run polls until every pending operation completes or the loop transitions to
// a fatal state. The returned []R is freshly allocated and owned by the
// caller, so it does not alias any internal buffer (unlike [Loop.Poll]). On a
// fatal failure Run returns the partial results accumulated so far together
// with the fatal error; subsequent SubmitExpr/Submit/Poll/Run calls return the
// same stored error.
func (l *Loop[B, R]) Run() ([]R, error) {
	if l.fatal != nil {
		return nil, l.fatal
	}
	pending := l.Pending()
	if pending == 0 {
		return nil, nil
	}
	all := make([]R, 0, pending)
	var bo iox.Backoff
	for pending > 0 {
		results, err, progress := l.step()
		if len(results) > 0 {
			all = append(all, results...)
			pending -= len(results)
		}
		if err != nil {
			return all, err
		}
		if l.fatal != nil {
			return all, l.fatal
		}
		if progress {
			bo.Reset()
		} else {
			bo.Wait()
		}
	}
	return all, nil
}
