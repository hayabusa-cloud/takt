// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"errors"
	"testing"

	"code.hybscloud.com/cove"
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

func TestStepPure(t *testing.T) {
	m := kont.ExprReturn[int](42)
	result, susp := takt.Step[int](m)
	if susp != nil {
		t.Fatal("expected nil suspension for pure computation")
	}
	if result != 42 {
		t.Fatalf("got %d, want 42", result)
	}
}

func TestStepEffectful(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	result, susp := takt.Step[int](m)
	if susp == nil {
		t.Fatal("expected suspension for effectful computation")
	}
	if result != 0 {
		t.Fatalf("got %d, want 0 (zero value)", result)
	}
	if _, ok := susp.Op().(echoOp); !ok {
		t.Fatalf("expected echoOp, got %T", susp.Op())
	}
}

func TestAdvanceNil(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	_, susp := takt.Step[int](m)

	d := &testDispatcher{value: 99}
	result, next, err := takt.Advance(d, susp)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if next != nil {
		t.Fatal("expected nil suspension after single-effect computation")
	}
	if result != 99 {
		t.Fatalf("got %d, want 99", result)
	}
}

func TestAdvanceErrMore(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	_, susp := takt.Step[int](m)

	d := &errMoreDispatcher{value: 77}
	result, next, err := takt.Advance(d, susp)
	if !iox.IsMore(err) {
		t.Fatalf("expected ErrMore, got %v", err)
	}
	if next != nil {
		t.Fatal("expected nil suspension after single-effect computation")
	}
	if result != 77 {
		t.Fatalf("got %d, want 77", result)
	}
}

func TestAdvanceWouldBlock(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	_, susp := takt.Step[int](m)

	d := &wouldBlockDispatcher{value: 42, blocksN: 100}
	result, retrySusp, err := takt.Advance(d, susp)
	if !iox.IsWouldBlock(err) {
		t.Fatalf("expected ErrWouldBlock, got %v", err)
	}
	if retrySusp != susp {
		t.Fatal("suspension should be returned unconsumed on WouldBlock")
	}
	if result != 0 {
		t.Fatalf("got %d, want 0 (zero value)", result)
	}
}

func TestAdvanceFailure(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	_, susp := takt.Step[int](m)

	d := &failDispatcher{}
	result, retrySusp, err := takt.Advance(d, susp)
	if err == nil {
		t.Fatal("expected error on failure")
	}
	if errors.Is(err, iox.ErrWouldBlock) || errors.Is(err, iox.ErrMore) {
		t.Fatalf("expected non-iox error, got %v", err)
	}
	if retrySusp != susp {
		t.Fatal("suspension should be returned unconsumed on failure")
	}
	if result != 0 {
		t.Fatalf("got %d, want 0 (zero value)", result)
	}
}

func TestAdvanceSuspensionWithContextPreservesContext(t *testing.T) {
	cont := kont.Bind(kont.Perform(echoOp{}), func(v int) kont.Eff[int] {
		return kont.Bind(kont.Perform(echoOp{}), func(w int) kont.Eff[int] {
			return kont.Pure(v + w)
		})
	})

	_, step := cove.StepWith("ctx", cont)
	if step.Suspension == nil {
		t.Fatal("expected suspension")
	}

	d := &testDispatcher{value: 10}
	result, next, err := takt.AdvanceSuspension(d, step)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result != 0 {
		t.Fatalf("got %d, want 0 (zero value)", result)
	}
	if next.Suspension == nil {
		t.Fatal("expected next suspension")
	}
	if next.Ask() != "ctx" {
		t.Fatalf("unexpected ctx: %v", next.Ask())
	}

	result, done, err := takt.AdvanceSuspension(d, next)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if done.Suspension != nil {
		t.Fatal("expected completion after second advance")
	}
	if done.Ask() != "ctx" {
		t.Fatalf("unexpected completion ctx: %q", done.Ask())
	}
	if result != 20 {
		t.Fatalf("got %d, want 20", result)
	}
}

func TestAdvanceSuspensionWouldBlockReturnsObservedSuspension(t *testing.T) {
	_, step := cove.StepExprWith("ctx", kont.ExprPerform(echoOp{}))
	if step.Suspension == nil {
		t.Fatal("expected suspension")
	}

	d := &wouldBlockDispatcher{value: 42, blocksN: 1}
	result, retry, err := takt.AdvanceSuspension(d, step)
	if !iox.IsWouldBlock(err) {
		t.Fatalf("expected ErrWouldBlock, got %v", err)
	}
	if result != 0 {
		t.Fatalf("got %d, want 0 (zero value)", result)
	}
	if retry.Suspension != step.Suspension {
		t.Fatal("expected suspension to remain unconsumed")
	}
	if retry.Ask() != "ctx" {
		t.Fatalf("unexpected ctx: %v", retry.Ask())
	}
}

func TestSuspensionViewSatisfiesSuspensionLike(t *testing.T) {
	step := cove.ObserveSuspension("ctx", &kont.Suspension[int]{})
	var _ takt.SuspensionLike[cove.SuspensionView[string, int], int] = step
}

func TestFullStepLoop(t *testing.T) {
	// Multi-step: echo three times, sum results
	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprBind(kont.ExprPerform(echoOp{}), func(c int) kont.Expr[int] {
				return kont.ExprReturn[int](a + b + c)
			})
		})
	})

	d := &testDispatcher{value: 10}
	result, susp := takt.Step[int](m)
	for susp != nil {
		var err error
		result, susp, err = takt.Advance(d, susp)
		if err != nil {
			t.Fatalf("Advance error: %v", err)
		}
	}
	if result != 30 {
		t.Fatalf("got %d, want 30", result)
	}
}
