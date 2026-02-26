// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"errors"
	"testing"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

// immediateBackend dispatches operations synchronously via a dispatch function.
// Submit queues the completion; Poll returns queued completions.
type immediateBackend struct {
	dispatch func(op kont.Operation) (kont.Resumed, error)
	nextTok  takt.Token
	ready    []takt.Completion
}

func (b *immediateBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	v, err := b.dispatch(op)
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: v,
		Err:   err,
	})
	return tok, nil
}

func (b *immediateBackend) Poll(completions []takt.Completion) int {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n
}

// failSubmitBackend fails on the Nth Submit call.
type failSubmitBackend struct {
	dispatch func(op kont.Operation) (kont.Resumed, error)
	nextTok  takt.Token
	ready    []takt.Completion
	failAt   int
	submits  int
}

func (b *failSubmitBackend) Submit(op kont.Operation) (takt.Token, error) {
	b.submits++
	if b.submits == b.failAt {
		return 0, errors.New("takt_test: submit failed")
	}
	tok := b.nextTok
	b.nextTok++
	v, err := b.dispatch(op)
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: v,
		Err:   err,
	})
	return tok, nil
}

func (b *failSubmitBackend) Poll(completions []takt.Completion) int {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n
}

// errMoreBackend returns ErrMore on first completion, then nil on resubmit.
type errMoreBackend struct {
	nextTok takt.Token
	ready   []takt.Completion
}

func (b *errMoreBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	if _, ok := op.(echoOp); ok {
		b.ready = append(b.ready, takt.Completion{
			Token: tok,
			Value: 10,
			Err:   iox.ErrMore,
		})
	}
	return tok, nil
}

func (b *errMoreBackend) Poll(completions []takt.Completion) int {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n
}

// failureBackend returns a failure error in completions.
type failureBackend struct {
	nextTok takt.Token
	ready   []takt.Completion
}

func (b *failureBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	// On failure, Value is still valid per backend contract.
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: -1,
		Err:   errors.New("takt_test: device error"),
	})
	return tok, nil
}

func (b *failureBackend) Poll(completions []takt.Completion) int {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n
}

func TestSubmitExprPure(t *testing.T) {
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, 16)
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
	l := takt.NewLoop[*immediateBackend, int](b, 16)
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
	l := takt.NewLoop[*failSubmitBackend, int](b, 16)
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
	l := takt.NewLoop[*immediateBackend, int](b, 16)
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
	l := takt.NewLoop[*errMoreBackend, int](b, 16)

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
	l := takt.NewLoop[*failureBackend, int](b, 16)

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

func TestPollResubmitsNextSuspension(t *testing.T) {
	d := &testDispatcher{value: 10}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, 16)

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
	l := takt.NewLoop[*failSubmitBackend, int](b, 16)

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

func TestRunDrivesToCompletion(t *testing.T) {
	d := &testDispatcher{value: 10}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, 16)

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

func TestMultipleConcurrentSubmissions(t *testing.T) {
	d := &testDispatcher{value: 5}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, 16)

	for i := 0; i < 3; i++ {
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
	l := takt.NewLoop[*immediateBackend, int](b, 16)

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
	l := takt.NewLoop[*immediateBackend, int](b, 16)

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

func TestPollErrMoreMultiStep(t *testing.T) {
	// ErrMore keeps token: after resume, if computation suspends again,
	// the token should still map to the new suspension.
	b := &errMoreBackend{}
	l := takt.NewLoop[*errMoreBackend, int](b, 16)

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
	l := takt.NewLoop[*immediateBackend, int](b, 16)

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

// multishotBackend queues two completions per submit: first ErrMore, then nil.
// Simulates a multishot backend where a single token produces multiple completions.
type multishotBackend struct {
	nextTok takt.Token
	ready   []takt.Completion
}

func (b *multishotBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	// Queue two completions for the same token: ErrMore then OK
	b.ready = append(b.ready,
		takt.Completion{Token: tok, Value: 10, Err: iox.ErrMore},
		takt.Completion{Token: tok, Value: 20, Err: nil},
	)
	return tok, nil
}

func (b *multishotBackend) Poll(completions []takt.Completion) int {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n
}

func TestPollErrMoreResuspends(t *testing.T) {
	// ErrMore completion resumes, but computation suspends again on next effect.
	// Covers the ErrMore path where next != nil (re-maps token).
	b := &multishotBackend{}
	l := takt.NewLoop[*multishotBackend, int](b, 1) // maxCompletions=1 so we poll one completion at a time

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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0 (resuspended)", len(results))
	}
	if l.Pending() != 1 {
		t.Fatalf("pending %d, want 1", l.Pending())
	}

	// Second poll: OK completion for same token → resubmits next op
	results, err = l.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After resume of second effect, computation suspends again with new submit
	// The multishot backend queues 2 more completions for the new token
	// Run to drain
	all := results
	for l.Pending() > 0 {
		results, err = l.Poll()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		all = append(all, results...)
	}
	if len(all) != 1 {
		t.Fatalf("got %d results, want 1", len(all))
	}
	if all[0] != 30 {
		t.Fatalf("got %d, want 30 (10+20)", all[0])
	}
}

func TestRunResubmitError(t *testing.T) {
	// Run encounters a resubmit error during Poll.
	d := &testDispatcher{value: 10}
	b := &failSubmitBackend{dispatch: d.Dispatch, failAt: 2}
	l := takt.NewLoop[*failSubmitBackend, int](b, 16)

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

func TestPollUnknownTokenSkipped(t *testing.T) {
	// Poll with a completion for an unknown token should be silently skipped
	d := &testDispatcher{value: 42}
	b := &immediateBackend{dispatch: d.Dispatch}
	l := takt.NewLoop[*immediateBackend, int](b, 16)

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
	loop := takt.NewLoop[*benchBackend, int](backend, 1)
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
