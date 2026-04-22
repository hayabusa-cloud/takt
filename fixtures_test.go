// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

// Shared Dispatcher / Operation test doubles for the takt test suite.
// Backend doubles live in backends_test.go; the test helpers are split this
// way so each source file gets its behavior-focused test file (takt_test.go,
// step_test.go, error_test.go, loop_test.go, completion_memory_test.go) without
// duplicating fixtures.

import (
	"errors"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

// echoOp is the basic typed test operation: it carries no payload
// and resumes with whatever int the dispatcher chooses to return.
type echoOp struct{ kont.Phantom[int] }

// testDispatcher dispatches echoOp by returning a configurable value.
type testDispatcher struct{ value int }

func (d *testDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	if _, ok := op.(echoOp); ok {
		return d.value, nil
	}
	return nil, errors.New("takt_test: unknown op")
}

// wouldBlockDispatcher returns ErrWouldBlock blocksN times, then succeeds.
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

// errMoreDispatcher returns ErrMore on the first call, nil on subsequent calls.
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

// failDispatcher always returns an infrastructure failure.
type failDispatcher struct{}

func (d *failDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	return nil, errors.New("takt_test: infrastructure failure")
}

// echoChain builds an n-step Expr program that performs echoOp n times and
// sums the dispatcher-supplied values. Used by both Loop tests and property
// tests to exercise multi-step suspensions deterministically.
func echoChain(n int) kont.Expr[int] {
	if n <= 0 {
		return kont.ExprReturn(0)
	}
	var expr kont.Expr[int] = kont.ExprPerform(echoOp{})
	for i := 1; i < n; i++ {
		prev := expr
		expr = kont.ExprBind(prev, func(total int) kont.Expr[int] {
			return kont.ExprBind(kont.ExprPerform(echoOp{}), func(v int) kont.Expr[int] {
				return kont.ExprReturn(total + v)
			})
		})
	}
	return expr
}
