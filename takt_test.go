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

// echoOp is a test operation that returns a fixed int.
type echoOp struct{ kont.Phantom[int] }

// testDispatcher dispatches echoOp by returning a configurable value.
type testDispatcher struct{ value int }

func (d *testDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	if _, ok := op.(echoOp); ok {
		return d.value, nil
	}
	return nil, errors.New("takt_test: unknown op")
}

// wouldBlockDispatcher returns ErrWouldBlock N times then succeeds.
type wouldBlockDispatcher struct {
	value   int
	blocksN int
	count   int
}

func (d *wouldBlockDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	if _, ok := op.(echoOp); ok {
		if d.count < d.blocksN {
			d.count++
			return nil, iox.ErrWouldBlock
		}
		return d.value, nil
	}
	return nil, errors.New("takt_test: unknown op")
}

// errMoreDispatcher returns ErrMore on first call, nil on second.
type errMoreDispatcher struct {
	value int
	calls int
}

func (d *errMoreDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	if _, ok := op.(echoOp); ok {
		d.calls++
		if d.calls == 1 {
			return d.value, iox.ErrMore
		}
		return d.value + 1, nil
	}
	return nil, errors.New("takt_test: unknown op")
}

// failDispatcher always returns a failure error.
type failDispatcher struct{}

func (d *failDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	return nil, errors.New("takt_test: infrastructure failure")
}

func TestExecPure(t *testing.T) {
	d := &testDispatcher{value: 42}
	result := takt.Exec(d, kont.Pure(99))
	if result != 99 {
		t.Fatalf("got %d, want 99", result)
	}
}

func TestExecExprPure(t *testing.T) {
	d := &testDispatcher{value: 42}
	result := takt.ExecExpr(d, kont.ExprReturn[int](99))
	if result != 99 {
		t.Fatalf("got %d, want 99", result)
	}
}

func TestExecSingleEffect(t *testing.T) {
	d := &testDispatcher{value: 42}
	m := kont.Perform(echoOp{})
	result := takt.Exec(d, m)
	if result != 42 {
		t.Fatalf("got %d, want 42", result)
	}
}

func TestExecExprSingleEffect(t *testing.T) {
	d := &testDispatcher{value: 42}
	m := kont.ExprPerform(echoOp{})
	result := takt.ExecExpr(d, m)
	if result != 42 {
		t.Fatalf("got %d, want 42", result)
	}
}

func TestExecMultiStepChain(t *testing.T) {
	d := &testDispatcher{value: 10}
	m := kont.Bind(kont.Perform(echoOp{}), func(a int) kont.Eff[int] {
		return kont.Bind(kont.Perform(echoOp{}), func(b int) kont.Eff[int] {
			return kont.Pure(a + b)
		})
	})
	result := takt.Exec(d, m)
	if result != 20 {
		t.Fatalf("got %d, want 20", result)
	}
}

func TestExecWaitsPastWouldBlock(t *testing.T) {
	d := &wouldBlockDispatcher{value: 42, blocksN: 5}
	m := kont.Perform(echoOp{})
	result := takt.Exec(d, m)
	if result != 42 {
		t.Fatalf("got %d, want 42", result)
	}
}

func TestExecErrMoreResumes(t *testing.T) {
	d := &errMoreDispatcher{value: 10}
	// Two effects: first returns ErrMore (value 10), second returns nil (value 11)
	m := kont.Bind(kont.Perform(echoOp{}), func(a int) kont.Eff[int] {
		return kont.Bind(kont.Perform(echoOp{}), func(b int) kont.Eff[int] {
			return kont.Pure(a + b)
		})
	})
	result := takt.Exec(d, m)
	if result != 21 {
		t.Fatalf("got %d, want 21", result)
	}
}

func TestExecFailurePanics(t *testing.T) {
	d := &failDispatcher{}
	m := kont.Perform(echoOp{})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on failure")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("unexpected panic type: %T", r)
		}
		if msg != "takt: dispatch failed: takt_test: infrastructure failure" {
			t.Fatalf("unexpected panic message: %s", msg)
		}
	}()
	takt.Exec(d, m)
}

func TestExecUnhandledOpPanics(t *testing.T) {
	type bogus struct{ kont.Phantom[int] }

	// testDispatcher returns error for unknown ops, handler panics on failure
	d := &testDispatcher{value: 42}
	m := kont.Perform(bogus{})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on unhandled op")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("unexpected panic type: %T", r)
		}
		if msg != "takt: dispatch failed: takt_test: unknown op" {
			t.Fatalf("unexpected panic message: %s", msg)
		}
	}()
	takt.Exec(d, m)
}
