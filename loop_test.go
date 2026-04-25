// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"errors"
	"testing"
	"time"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

func TestSubmitExprPure(t *testing.T) {
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))
	result, done, err := l.SubmitExpr(kont.ExprReturn[int](99))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected immediate completion for pure computation")
	}
	if result != 99 {
		t.Fatalf("got %d, want 99", result)
	}
	if l.Pending() != 0 {
		t.Fatalf("pending %d, want 0", l.Pending())
	}
}

func TestSubmitEffectful(t *testing.T) {
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))
	_, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("expected suspension, not immediate completion")
	}
	if l.Pending() != 1 {
		t.Fatalf("pending %d, want 1", l.Pending())
	}
}

func TestSubmitError(t *testing.T) {
	d := &testDispatcher{value: 42}
	b := &failSubmitBackend{dispatch: d.Dispatch, failAt: 1}
	l := takt.NewLoop[*failSubmitBackend, int](b, takt.WithMaxCompletions(16))
	_, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{}))
	if err == nil {
		t.Fatal("expected submit error")
	}
	if done {
		t.Fatal("expected failure, not completion")
	}
	if l.Pending() != 0 {
		t.Fatalf("pending %d, want 0", l.Pending())
	}
}

func TestPollDispatchesCompletion(t *testing.T) {
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))
	l.SubmitExpr(kont.ExprPerform(echoOp{}))

	results, err := l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 42 {
		t.Fatalf("got %d, want 42", results[0])
	}
	if l.Pending() != 0 {
		t.Fatalf("pending %d, want 0", l.Pending())
	}
}

func TestPollErrMoreKeepsToken(t *testing.T) {
	b := &errMoreBackend{}
	l := takt.NewLoop[*errMoreBackend, int](b, takt.WithMaxCompletions(16))

	// Multi-step: first effect gets ErrMore completion
	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprReturn[int](a + 5)
	})
	l.SubmitExpr(m)
	if l.Pending() != 1 {
		t.Fatalf("pending %d, want 1", l.Pending())
	}

	// Poll: ErrMore completion resumes, computation completes (a+5=15)
	results, err := l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 15 {
		t.Fatalf("got %d, want 15", results[0])
	}
}

func TestPollFailureResumesWithValue(t *testing.T) {
	b := &failureBackend{}
	l := takt.NewLoop[*failureBackend, int](b, takt.WithMaxCompletions(16))

	// Single effect: failure completion resumes with Value (-1)
	l.SubmitExpr(kont.ExprPerform(echoOp{}))

	results, err := l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != -1 {
		t.Fatalf("got %d, want -1", results[0])
	}
}

func TestPollWouldBlockIsIdle(t *testing.T) {
	b := &pollErrorBackend{pollErr: iox.ErrWouldBlock}
	l := takt.NewLoop[*pollErrorBackend, int](b, takt.WithMaxCompletions(16))

	l.SubmitExpr(kont.ExprPerform(echoOp{}))

	results, err := l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
	if results != nil {
		t.Fatalf("idle Poll results = %#v, want nil", results)
	}
	if l.Pending() != 1 {
		t.Fatalf("pending %d, want 1", l.Pending())
	}
}

func TestPollCompletionWouldBlockResubmitsOperation(t *testing.T) {
	b := &wouldBlockCompletionBackend{}
	l := takt.NewLoop[*wouldBlockCompletionBackend, int](b, takt.WithMaxCompletions(16))

	l.SubmitExpr(kont.ExprPerform(echoOp{}))

	results, err := l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
	// WouldBlock re-arms the suspension but remains a no-progress observation,
	// so Poll reports the idle nil-slice sentinel.
	if results != nil {
		t.Fatalf("Poll after completion-WouldBlock resubmit results = %#v, want nil idle sentinel", results)
	}
	if l.Pending() != 1 {
		t.Fatalf("pending %d, want 1", l.Pending())
	}
	if b.attempts != 2 {
		t.Fatalf("submit attempts = %d, want 2", b.attempts)
	}

	results, err = l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 10 {
		t.Fatalf("got %d, want 10", results[0])
	}
}

func TestRunHandlesCompletionWouldBlockResubmit(t *testing.T) {
	b := &wouldBlockCompletionBackend{}
	l := takt.NewLoop[*wouldBlockCompletionBackend, int](b, takt.WithMaxCompletions(16))

	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("submit = (_, %v, %v), want pending with nil error", done, err)
	}

	results, err := l.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0] != 10 {
		t.Fatalf("results = %#v, want [10]", results)
	}
	if b.attempts != 2 {
		t.Fatalf("submit attempts = %d, want 2", b.attempts)
	}
}

func TestPollCompletionWouldBlockResubmitErrorClearsPending(t *testing.T) {
	dispatches := 0
	b := &failSubmitBackend{
		failAt: 2,
		dispatch: func(op kont.Operation) (kont.Resumed, error) {
			dispatches++
			if dispatches == 1 {
				return nil, iox.ErrWouldBlock
			}
			return 10, nil
		},
	}
	l := takt.NewLoop[*failSubmitBackend, int](b, takt.WithMaxCompletions(16))

	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("submit = (_, %v, %v), want pending with nil error", done, err)
	}

	results, err := l.Poll()
	if err == nil {
		t.Fatal("expected resubmit error")
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
	if l.Pending() != 0 {
		t.Fatalf("pending %d, want 0", l.Pending())
	}
	if dispatches != 1 {
		t.Fatalf("dispatches = %d, want 1", dispatches)
	}
}

func TestPollBackendErrorReturned(t *testing.T) {
	pollErr := errors.New("takt_test: poll failed")
	b := &pollErrorBackend{pollErr: pollErr}
	l := takt.NewLoop[*pollErrorBackend, int](b, takt.WithMaxCompletions(16))

	l.SubmitExpr(kont.ExprPerform(echoOp{}))

	results, err := l.Poll()
	if err != pollErr {
		t.Fatalf("err = %v, want %v", err, pollErr)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
	// Affine cleanup: pending suspensions are drained on fatal so each
	// `*kont.Suspension[R]` is consumed exactly once.
	if l.Pending() != 0 {
		t.Fatalf("pending %d, want 0 (drained on fatal)", l.Pending())
	}
	if l.Failed() != pollErr {
		t.Fatalf("Failed() = %v, want %v", l.Failed(), pollErr)
	}
}

func TestPollAfterFatalReturnsStoredError(t *testing.T) {
	pollErr := errors.New("takt_test: poll failed")
	b := &pollErrorBackend{pollErr: pollErr}
	l := takt.NewLoop[*pollErrorBackend, int](b, takt.WithMaxCompletions(16))

	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("submit = (_, %v, %v), want pending with nil error", done, err)
	}

	results, err := l.Poll()
	if err != pollErr {
		t.Fatalf("first poll err = %v, want %v", err, pollErr)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}

	results, err = l.Poll()
	if err != pollErr {
		t.Fatalf("second poll err = %v, want %v", err, pollErr)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}

func TestSubmitExprAfterFatalReturnsStoredError(t *testing.T) {
	pollErr := errors.New("takt_test: poll failed")
	b := &pollErrorBackend{pollErr: pollErr}
	l := takt.NewLoop[*pollErrorBackend, int](b, takt.WithMaxCompletions(16))

	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("submit = (_, %v, %v), want pending with nil error", done, err)
	}
	if _, err := l.Poll(); err != pollErr {
		t.Fatalf("poll err = %v, want %v", err, pollErr)
	}

	result, done, err := l.SubmitExpr(kont.ExprReturn(99))
	if err != pollErr {
		t.Fatalf("submit after fatal err = %v, want %v", err, pollErr)
	}
	if done {
		t.Fatal("expected stored fatal error, not completion")
	}
	if result != 0 {
		t.Fatalf("result = %d, want 0", result)
	}
}

func TestPollResubmitsNextSuspension(t *testing.T) {
	d := &testDispatcher{value: 10}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	// Two effects chained
	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn[int](a + b)
		})
	})
	l.SubmitExpr(m)

	// First poll: dispatches first completion, resubmits second
	results, err := l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0 (resubmitted)", len(results))
	}
	if l.Pending() != 1 {
		t.Fatalf("pending %d, want 1", l.Pending())
	}

	// Second poll: dispatches second completion, computation completes
	results, err = l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 20 {
		t.Fatalf("got %d, want 20", results[0])
	}
}

func TestPollResubmitError(t *testing.T) {
	// Second submit fails (resubmit after first completion)
	d := &testDispatcher{value: 10}
	b := &failSubmitBackend{dispatch: d.Dispatch, failAt: 2}
	l := takt.NewLoop[*failSubmitBackend, int](b, takt.WithMaxCompletions(16))

	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn[int](a + b)
		})
	})
	l.SubmitExpr(m)

	// Poll: first completion → try resubmit → fail
	results, err := l.Poll()
	if err == nil {
		t.Fatal("expected resubmit error")
	}
	// Partial results may be empty (error happened during resubmit)
	_ = results
}

func TestPollReturnsResultsBeforeStoredFatal(t *testing.T) {
	d := &testDispatcher{value: 10}
	b := &failSubmitBackend{dispatch: d.Dispatch, failAt: 3}
	l := takt.NewLoop[*failSubmitBackend, int](b, takt.WithMaxCompletions(16))

	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("first submit = (_, %v, %v), want pending with nil error", done, err)
	}
	chained := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn(a + b)
		})
	})
	if _, done, err := l.SubmitExpr(chained); err != nil || done {
		t.Fatalf("second submit = (_, %v, %v), want pending with nil error", done, err)
	}

	results, err := l.Poll()
	if err != nil {
		t.Fatalf("first poll err = %v, want nil", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 10 {
		t.Fatalf("got %d, want 10", results[0])
	}

	results, err = l.Poll()
	if err == nil {
		t.Fatal("expected stored fatal error on next poll")
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}

func TestRunDrivesToCompletion(t *testing.T) {
	d := &testDispatcher{value: 10}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn[int](a + b)
		})
	})
	l.SubmitExpr(m)

	results, err := l.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 20 {
		t.Fatalf("got %d, want 20", results[0])
	}
}

func TestNewLoopRejectsNonPositiveMaxCompletions(t *testing.T) {
	for _, n := range []int{0, -1, -1024} {
		func(n int) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("NewLoop(%d) did not panic", n)
				}
				msg, ok := r.(string)
				if !ok || msg != "takt: WithMaxCompletions requires n > 0" {
					t.Fatalf("NewLoop(%d) panic = %v, want fixed message", n, r)
				}
			}()
			b := &immediateBackend{}
			_ = takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(n))
		}(n)
	}
}

func TestRunAfterDrainSurfacesFatal(t *testing.T) {
	b := &immediateBackend{}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(4))

	if drained := l.Drain(); drained != 0 {
		t.Fatalf("Drain on idle loop = %d, want 0", drained)
	}
	results, err := l.Run()
	if err != takt.ErrDisposed {
		t.Fatalf("Run after Drain err = %v, want ErrDisposed", err)
	}
	if results != nil {
		t.Fatalf("Run after Drain results = %#v, want nil", results)
	}
}

func TestRunWithNoPendingReturnsNil(t *testing.T) {
	b := &immediateBackend{}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	results, err := l.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatalf("got %#v, want nil", results)
	}
}

func TestFailedNilBeforeFatal(t *testing.T) {
	b := &immediateBackend{}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(4))
	if err := l.Failed(); err != nil {
		t.Fatalf("Failed() = %v, want nil before any fatal", err)
	}
}

func TestDrainBeforeFatalDiscardsPendingAndPoisonsLoop(t *testing.T) {
	d := &testDispatcher{value: 1}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(4))

	if _, _, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil {
		t.Fatalf("submit err = %v, want nil", err)
	}
	if _, _, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil {
		t.Fatalf("submit err = %v, want nil", err)
	}
	if l.Pending() != 2 {
		t.Fatalf("pending before drain = %d, want 2", l.Pending())
	}

	drained := l.Drain()
	if drained != 2 {
		t.Fatalf("Drain() = %d, want 2", drained)
	}
	if l.Pending() != 0 {
		t.Fatalf("pending after drain = %d, want 0", l.Pending())
	}
	if err := l.Failed(); err != takt.ErrDisposed {
		t.Fatalf("Failed() after drain = %v, want ErrDisposed", err)
	}

	// Drain is idempotent.
	if again := l.Drain(); again != 0 {
		t.Fatalf("second Drain() = %d, want 0 (idempotent)", again)
	}
	if err := l.Failed(); err != takt.ErrDisposed {
		t.Fatalf("Failed() after second drain = %v, want ErrDisposed", err)
	}

	// Subsequent Submit/Poll observe the disposed state.
	if _, _, err := l.SubmitExpr(kont.ExprReturn(0)); err != takt.ErrDisposed {
		t.Fatalf("SubmitExpr after drain = %v, want ErrDisposed", err)
	}
	if _, err := l.Poll(); err != takt.ErrDisposed {
		t.Fatalf("Poll after drain = %v, want ErrDisposed", err)
	}
}

func TestDrainAfterFatalPreservesOriginalError(t *testing.T) {
	pollErr := errors.New("takt_test: poll failed")
	b := &pollErrorBackend{pollErr: pollErr}
	l := takt.NewLoop[*pollErrorBackend, int](b, takt.WithMaxCompletions(4))

	if _, _, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil {
		t.Fatalf("submit err = %v, want nil", err)
	}
	if _, err := l.Poll(); err != pollErr {
		t.Fatalf("poll err = %v, want %v", err, pollErr)
	}
	// Pending was already drained on fatal; Drain returns 0 but must not
	// overwrite the original fatal error.
	if drained := l.Drain(); drained != 0 {
		t.Fatalf("Drain() after fatal = %d, want 0", drained)
	}
	if err := l.Failed(); err != pollErr {
		t.Fatalf("Failed() after Drain post-fatal = %v, want %v", err, pollErr)
	}
}

func TestDrainAfterFatalReleasesMemoryExactlyOnceAndPreservesError(t *testing.T) {
	pollErr := errors.New("takt_test: poll failed")
	mem := &recordingMemory{}
	b := &pollErrorBackend{pollErr: pollErr}
	l := takt.NewLoop[*pollErrorBackend, int](b, takt.WithMemory(mem),
		takt.WithMaxCompletions(4))

	if _, _, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil {
		t.Fatalf("submit err = %v, want nil", err)
	}
	if _, err := l.Poll(); err != pollErr {
		t.Fatalf("poll err = %v, want %v", err, pollErr)
	}
	if mem.released {
		t.Fatal("CompletionMemory.Release called before Drain")
	}

	if drained := l.Drain(); drained != 0 {
		t.Fatalf("Drain() after fatal = %d, want 0", drained)
	}
	if err := l.Failed(); err != pollErr {
		t.Fatalf("Failed() after Drain post-fatal = %v, want %v", err, pollErr)
	}
	if mem.releaseCalls != 1 {
		t.Fatalf("CompletionMemory.Release calls after fatal Drain = %d, want 1", mem.releaseCalls)
	}

	if drained := l.Drain(); drained != 0 {
		t.Fatalf("second Drain() after fatal = %d, want 0", drained)
	}
	if err := l.Failed(); err != pollErr {
		t.Fatalf("Failed() after second Drain post-fatal = %v, want %v", err, pollErr)
	}
	if mem.releaseCalls != 1 {
		t.Fatalf("CompletionMemory.Release calls after second fatal Drain = %d, want 1", mem.releaseCalls)
	}
}

func TestRunSkipsBackoffOnProgressOnlyPolls(t *testing.T) {
	d := &testDispatcher{value: 1}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	l.SubmitExpr(echoChain(60))

	start := time.Now()
	results, err := l.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 60 {
		t.Fatalf("got %d, want 60", results[0])
	}
	if elapsed := time.Since(start); elapsed > 170*time.Millisecond {
		t.Fatalf("Run took %v, want <= 170ms without idle backoff on progress-only polls", elapsed)
	}
}

func TestRunWaitsThroughIdlePolls(t *testing.T) {
	b := &idleThenReadyBackend{}
	l := takt.NewLoop[*idleThenReadyBackend, int](b, takt.WithMaxCompletions(16))

	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("submit = (_, %v, %v), want pending with nil error", done, err)
	}

	results, err := l.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 21 {
		t.Fatalf("got %d, want 21", results[0])
	}
	if b.polls != 2 {
		t.Fatalf("polls = %d, want 2", b.polls)
	}
}

func TestMultipleConcurrentSubmissions(t *testing.T) {
	d := &testDispatcher{value: 5}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	for range 3 {
		l.SubmitExpr(kont.ExprPerform(echoOp{}))
	}
	if l.Pending() != 3 {
		t.Fatalf("pending %d, want 3", l.Pending())
	}

	results, err := l.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for i, r := range results {
		if r != 5 {
			t.Fatalf("result[%d] = %d, want 5", i, r)
		}
	}
}

func TestPendingCount(t *testing.T) {
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	if l.Pending() != 0 {
		t.Fatalf("initial pending %d, want 0", l.Pending())
	}
	l.SubmitExpr(kont.ExprPerform(echoOp{}))
	if l.Pending() != 1 {
		t.Fatalf("after submit pending %d, want 1", l.Pending())
	}
	l.SubmitExpr(kont.ExprPerform(echoOp{}))
	if l.Pending() != 2 {
		t.Fatalf("after 2 submits pending %d, want 2", l.Pending())
	}
	l.Run()
	if l.Pending() != 0 {
		t.Fatalf("after run pending %d, want 0", l.Pending())
	}
}

func TestSubmit(t *testing.T) {
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	m := kont.Perform(echoOp{})
	_, done, err := l.Submit(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("expected suspension, not immediate completion")
	}

	results, runErr := l.Run()
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 42 {
		t.Fatalf("got %d, want 42", results[0])
	}
}

func TestSubmitExprDuplicateLiveTokenPoisonsLoop(t *testing.T) {
	b := &duplicateTokenBackend{
		tokens: []takt.Token{1, 1},
		value:  42,
	}
	l := takt.NewLoop[*duplicateTokenBackend, int](b, takt.WithMaxCompletions(16))

	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("first submit = (_, %v, %v), want pending with nil error", done, err)
	}

	result, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{}))
	if !errors.Is(err, takt.ErrLiveTokenReuse) {
		t.Fatalf("second submit err = %v, want %v", err, takt.ErrLiveTokenReuse)
	}
	if done {
		t.Fatal("expected fatal error, not completion")
	}
	if result != 0 {
		t.Fatalf("result = %d, want 0", result)
	}
	if l.Pending() != 0 {
		t.Fatalf("pending %d, want 0 after poison", l.Pending())
	}
	if !errors.Is(l.Failed(), takt.ErrLiveTokenReuse) {
		t.Fatalf("Failed() = %v, want %v", l.Failed(), takt.ErrLiveTokenReuse)
	}
	if _, pollErr := l.Poll(); !errors.Is(pollErr, takt.ErrLiveTokenReuse) {
		t.Fatalf("Poll after duplicate err = %v, want %v", pollErr, takt.ErrLiveTokenReuse)
	}
}

func TestPollErrMoreMultiStep(t *testing.T) {
	// ErrMore keeps token: after resume, if computation suspends again,
	// the token should still map to the new suspension.
	b := &errMoreBackend{}
	l := takt.NewLoop[*errMoreBackend, int](b, takt.WithMaxCompletions(16))

	// Single effect computation: ErrMore resume → immediate completion
	l.SubmitExpr(kont.ExprPerform(echoOp{}))

	results, err := l.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0] != 10 {
		t.Fatalf("got %d, want 10", results[0])
	}
}

func TestSubmitPure(t *testing.T) {
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	result, done, err := l.Submit(kont.Pure(99))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected immediate completion for pure computation")
	}
	if result != 99 {
		t.Fatalf("got %d, want 99", result)
	}
}

func TestPollErrMoreResuspendReturnsError(t *testing.T) {
	// ErrMore completion resumes, but computation suspends again on next effect.
	// The current Loop implementation cannot safely track both the live
	// multishot source and a new submitted effect under one correlation token.
	b := &multishotBackend{}
	l := takt.NewLoop[*multishotBackend, int](b, takt.WithMaxCompletions(1)) // maxCompletions=1 so we poll one completion at a time

	// Two effects chained: first gets ErrMore, resume yields second effect
	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn[int](a + b)
		})
	})
	l.SubmitExpr(m)
	if l.Pending() != 1 {
		t.Fatalf("pending %d, want 1", l.Pending())
	}

	// First poll (maxCompletions=1): ErrMore resume → second effect suspends → token kept
	results, err := l.Poll()
	if !errors.Is(err, takt.ErrUnsupportedMultishot) {
		t.Fatalf("err = %v, want %v", err, takt.ErrUnsupportedMultishot)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
	if l.Pending() != 0 {
		t.Fatalf("pending %d, want 0", l.Pending())
	}
}

func TestPollResubmitDuplicateLiveTokenPoisonsLoop(t *testing.T) {
	b := &duplicateTokenBackend{
		tokens: []takt.Token{1, 2, 2},
		value:  10,
	}
	l := takt.NewLoop[*duplicateTokenBackend, int](b, takt.WithMaxCompletions(1))

	chained := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn(a + b)
		})
	})
	if _, done, err := l.SubmitExpr(chained); err != nil || done {
		t.Fatalf("first submit = (_, %v, %v), want pending with nil error", done, err)
	}
	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("second submit = (_, %v, %v), want pending with nil error", done, err)
	}

	results, err := l.Poll()
	if !errors.Is(err, takt.ErrLiveTokenReuse) {
		t.Fatalf("Poll err = %v, want %v", err, takt.ErrLiveTokenReuse)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
	if l.Pending() != 0 {
		t.Fatalf("pending %d, want 0 after poison", l.Pending())
	}
	if !errors.Is(l.Failed(), takt.ErrLiveTokenReuse) {
		t.Fatalf("Failed() = %v, want %v", l.Failed(), takt.ErrLiveTokenReuse)
	}
}

func TestRunResubmitError(t *testing.T) {
	// Run encounters a resubmit error during Poll.
	d := &testDispatcher{value: 10}
	b := &failSubmitBackend{dispatch: d.Dispatch, failAt: 2}
	l := takt.NewLoop[*failSubmitBackend, int](b, takt.WithMaxCompletions(16))

	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn[int](a + b)
		})
	})
	l.SubmitExpr(m)

	results, err := l.Run()
	if err == nil {
		t.Fatal("expected error from Run")
	}
	// Partial results: none completed before the error
	_ = results
}

func TestRunReturnsBackendPollError(t *testing.T) {
	pollErr := errors.New("takt_test: poll failed")
	b := &pollErrorBackend{pollErr: pollErr}
	l := takt.NewLoop[*pollErrorBackend, int](b, takt.WithMaxCompletions(16))

	l.SubmitExpr(kont.ExprPerform(echoOp{}))

	results, err := l.Run()
	if err != pollErr {
		t.Fatalf("err = %v, want %v", err, pollErr)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}

func TestRunReturnsFatalAlongsidePartialResults(t *testing.T) {
	d := &testDispatcher{value: 10}
	b := &failSubmitBackend{dispatch: d.Dispatch, failAt: 3}
	l := takt.NewLoop[*failSubmitBackend, int](b, takt.WithMaxCompletions(16))

	if _, done, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
		t.Fatalf("first SubmitExpr = (_, %v, %v), want suspension", done, err)
	}
	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn[int](a + b)
		})
	})
	if _, done, err := l.SubmitExpr(m); err != nil || done {
		t.Fatalf("second SubmitExpr = (_, %v, %v), want suspension", done, err)
	}

	results, err := l.Run()
	if err == nil {
		t.Fatal("expected fatal error from Run")
	}
	if len(results) != 1 || results[0] != 10 {
		t.Fatalf("results = %#v, want [10]", results)
	}
	if l.Failed() == nil {
		t.Fatal("Failed() = nil, want stored fatal")
	}
}

func TestPollUnknownTokenSkipped(t *testing.T) {
	// Poll with a completion for an unknown token should be silently skipped
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, takt.WithMaxCompletions(16))

	// Submit, then manually clear pending so next poll finds orphan completion
	l.SubmitExpr(kont.ExprPerform(echoOp{}))
	// Run to drain the pending map
	l.Run()

	// Now inject a completion with an unknown token
	b.ready = append(b.ready, takt.Completion{
		Token: 999,
		Value: 0,
		Err:   nil,
	})
	results, err := l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0 for unknown token", len(results))
	}
}

func TestLoopSubmitPureAllocations(t *testing.T) {
	backend := &benchBackend{}
	loop := takt.NewLoop[*benchBackend, int](backend, takt.WithMaxCompletions(1))
	expr := kont.ExprReturn(42)

	allocs := testing.AllocsPerRun(100, func() {
		res, done, _ := loop.SubmitExpr(expr)
		if !done || res != 42 {
			t.Fatal("unexpected result")
		}
	})
	if allocs > 0 {
		t.Fatalf("expected 0 allocations, got %f", allocs)
	}
}
