// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"testing"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

func TestExecErrorSuccess(t *testing.T) {
	d := &testDispatcher{value: 42}
	m := kont.Bind(kont.Perform(echoOp{}), func(n int) kont.Eff[int] {
		return kont.Pure(n + 1)
	})
	result := takt.ExecError[string](d, m)
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != 43 {
		t.Fatalf("got %d, want 43", rv)
	}
}

func TestExecErrorThrow(t *testing.T) {
	d := &testDispatcher{value: 42}
	m := kont.Bind(kont.Perform(echoOp{}), func(n int) kont.Eff[int] {
		return kont.ThrowError[string, int]("boom")
	})
	result := takt.ExecError[string](d, m)
	if !result.IsLeft() {
		t.Fatal("expected Left")
	}
	errVal, _ := result.GetLeft()
	if errVal != "boom" {
		t.Fatalf("got %q, want %q", errVal, "boom")
	}
}

func TestExecErrorCatchRecovery(t *testing.T) {
	d := &testDispatcher{value: 10}
	m := kont.Bind(
		kont.CatchError(
			kont.ThrowError[string, string]("fail"),
			func(e string) kont.Eff[string] {
				return kont.Pure("recovered: " + e)
			},
		),
		func(s string) kont.Eff[string] {
			return kont.Bind(kont.Perform(echoOp{}), func(n int) kont.Eff[string] {
				return kont.Pure(s)
			})
		},
	)
	result := takt.ExecError[string](d, m)
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != "recovered: fail" {
		t.Fatalf("got %q, want %q", rv, "recovered: fail")
	}
}

func TestExecErrorExprSuccess(t *testing.T) {
	d := &testDispatcher{value: 42}
	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(n int) kont.Expr[int] {
		return kont.ExprReturn[int](n + 1)
	})
	result := takt.ExecErrorExpr[string](d, m)
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != 43 {
		t.Fatalf("got %d, want 43", rv)
	}
}

func TestExecErrorExprThrow(t *testing.T) {
	d := &testDispatcher{value: 42}
	m := kont.ExprBind(kont.ExprPerform(echoOp{}), func(n int) kont.Expr[int] {
		return kont.ExprThrowError[string, int]("expr-boom")
	})
	result := takt.ExecErrorExpr[string](d, m)
	if !result.IsLeft() {
		t.Fatal("expected Left")
	}
	errVal, _ := result.GetLeft()
	if errVal != "expr-boom" {
		t.Fatalf("got %q, want %q", errVal, "expr-boom")
	}
}

func TestStepErrorPure(t *testing.T) {
	m := kont.ExprReturn[int](42)
	result, susp := takt.StepError[string, int](m)
	if susp != nil {
		t.Fatal("expected nil suspension for pure computation")
	}
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != 42 {
		t.Fatalf("got %d, want 42", rv)
	}
}

func TestAdvanceErrorDispatcherNil(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	wrapped := kont.ExprMap(m, func(n int) kont.Either[string, int] {
		return kont.Right[string, int](n)
	})
	_, susp := kont.StepExpr(wrapped)
	if susp == nil {
		t.Fatal("expected suspension")
	}

	d := &testDispatcher{value: 99}
	result, next, err := takt.AdvanceError[string](d, susp)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if next != nil {
		t.Fatal("expected nil suspension")
	}
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != 99 {
		t.Fatalf("got %d, want 99", rv)
	}
}

func TestAdvanceErrorDispatcherErrMore(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	wrapped := kont.ExprMap(m, func(n int) kont.Either[string, int] {
		return kont.Right[string, int](n)
	})
	_, susp := kont.StepExpr(wrapped)
	if susp == nil {
		t.Fatal("expected suspension")
	}

	d := &errMoreDispatcher{value: 77}
	result, next, err := takt.AdvanceError[string](d, susp)
	if !iox.IsMore(err) {
		t.Fatalf("expected ErrMore, got %v", err)
	}
	if next != nil {
		t.Fatal("expected nil suspension")
	}
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != 77 {
		t.Fatalf("got %d, want 77", rv)
	}
}

func TestAdvanceErrorThrow(t *testing.T) {
	m := kont.ExprThrowError[string, int]("thrown")
	wrapped := kont.ExprMap(m, func(n int) kont.Either[string, int] {
		return kont.Right[string, int](n)
	})
	_, susp := kont.StepExpr(wrapped)
	if susp == nil {
		t.Fatal("expected suspension")
	}

	d := &testDispatcher{value: 42}
	result, next, err := takt.AdvanceError[string](d, susp)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if next != nil {
		t.Fatal("expected nil suspension after throw")
	}
	if !result.IsLeft() {
		t.Fatal("expected Left")
	}
	errVal, _ := result.GetLeft()
	if errVal != "thrown" {
		t.Fatalf("got %q, want %q", errVal, "thrown")
	}
}

func TestAdvanceErrorWouldBlock(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	wrapped := kont.ExprMap(m, func(n int) kont.Either[string, int] {
		return kont.Right[string, int](n)
	})
	_, susp := kont.StepExpr(wrapped)
	if susp == nil {
		t.Fatal("expected suspension")
	}

	d := &wouldBlockDispatcher{value: 42, blocksN: 100}
	_, retrySusp, err := takt.AdvanceError[string](d, susp)
	if !iox.IsWouldBlock(err) {
		t.Fatalf("expected ErrWouldBlock, got %v", err)
	}
	if retrySusp != susp {
		t.Fatal("suspension should be returned unconsumed on WouldBlock")
	}
}

func TestAdvanceErrorFailure(t *testing.T) {
	m := kont.ExprPerform(echoOp{})
	wrapped := kont.ExprMap(m, func(n int) kont.Either[string, int] {
		return kont.Right[string, int](n)
	})
	_, susp := kont.StepExpr(wrapped)
	if susp == nil {
		t.Fatal("expected suspension")
	}

	d := &failDispatcher{}
	_, retrySusp, err := takt.AdvanceError[string](d, susp)
	if err == nil {
		t.Fatal("expected error on failure")
	}
	if retrySusp != susp {
		t.Fatal("suspension should be returned unconsumed on failure")
	}
}

func TestCombinedDispatcherErrorChain(t *testing.T) {
	// Multi-step: dispatcher effect → error throw → catch recovery
	d := &testDispatcher{value: 42}
	m := kont.Bind(kont.Perform(echoOp{}), func(n int) kont.Eff[int] {
		return kont.CatchError(
			kont.ThrowError[string, int]("oops"),
			func(e string) kont.Eff[int] {
				return kont.Pure(n + 100)
			},
		)
	})
	result := takt.ExecError[string](d, m)
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != 142 {
		t.Fatalf("got %d, want 142", rv)
	}
}

func TestAdvanceErrorCatchStepping(t *testing.T) {
	// Stepping through Catch that succeeds
	body := kont.Pure[string]("ok")
	caught := kont.CatchError[string](body, func(e string) kont.Eff[string] {
		return kont.Pure("caught: " + e)
	})
	protocol := takt.Reify(caught)

	result, susp := takt.StepError[string, string](protocol)
	if susp == nil {
		t.Fatalf("expected suspension for Catch, got result %v", result)
	}

	d := &testDispatcher{value: 42}
	for susp != nil {
		var err error
		result, susp, err = takt.AdvanceError[string](d, susp)
		if err != nil {
			t.Fatalf("AdvanceError error: %v", err)
		}
	}
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != "ok" {
		t.Fatalf("got %q, want %q", rv, "ok")
	}
}

func TestExecErrorWaitsPastWouldBlock(t *testing.T) {
	d := &wouldBlockDispatcher{value: 42, blocksN: 3}
	m := kont.Perform(echoOp{})
	result := takt.ExecError[string](d, m)
	if !result.IsRight() {
		t.Fatal("expected Right")
	}
	rv, _ := result.GetRight()
	if rv != 42 {
		t.Fatalf("got %d, want 42", rv)
	}
}

func TestExecErrorFailurePanics(t *testing.T) {
	d := &failDispatcher{}
	m := kont.Perform(echoOp{})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on failure")
		}
	}()
	takt.ExecError[string](d, m)
}
