// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"errors"
	"math/rand/v2"
	"sort"
	"testing"
	"testing/quick"

	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

type propBackend struct {
	nextT      takt.Token
	pending    []takt.Completion
	shuffle    bool
	failSubmit bool
}

func (b *propBackend) Submit(op kont.Operation) (takt.Token, error) {
	if b.failSubmit {
		return 0, errors.New("simulated submit error")
	}
	t := b.nextT
	b.nextT++
	b.pending = append(b.pending, takt.Completion{
		Token: t,
		Value: kont.Erased(int(t)),
		Err:   nil,
	})
	return t, nil
}

func (b *propBackend) Poll(completions []takt.Completion) (int, error) {
	if len(b.pending) == 0 {
		return 0, nil
	}
	if b.shuffle {
		rand.Shuffle(len(b.pending), func(i, j int) {
			b.pending[i], b.pending[j] = b.pending[j], b.pending[i]
		})
	}
	n := copy(completions, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

type propOp struct{ kont.Phantom[int] }

func propCont() kont.Expr[int] {
	return kont.ExprBind(
		kont.ExprPerform(propOp{}),
		func(v int) kont.Expr[int] {
			return kont.ExprReturn(v)
		},
	)
}

func TestPropertyFairnessOrdering(t *testing.T) {
	f := func(count int) bool {
		if count < 0 {
			count = -count
		}
		count = (count % 500) + 1 // 1 to 500 items

		backend := &propBackend{nextT: 1, shuffle: true}
		loop := takt.NewLoop[*propBackend, int](backend, 1024, takt.WithMaxCompletions(count))

		for range count {
			loop.SubmitExpr(propCont())
		}

		var results []int
		for len(results) < count {
			res, _ := loop.Poll()
			results = append(results, res...)
		}

		sort.Ints(results)
		for i := range count {
			if results[i] != i+1 {
				return false
			}
		}

		// Ensure nothing is leaked
		res, _ := loop.Poll()
		if len(res) != 0 {
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}

// TestPropertyExecLoopBisimulation checks that, for a deterministic
// dispatcher and a deterministic program, the three execution modes produce
// the same final result: Exec, the Step+Advance stepping loop, and
// Loop.Submit+Run.
func TestPropertyExecLoopBisimulation(t *testing.T) {
	f := func(seed int) bool {
		if seed < 0 {
			seed = -seed
		}
		depth := (seed % 16) + 1

		// Mode 1: Exec (blocking handler over a deterministic dispatcher).
		dExec := &testDispatcher{value: 7}
		execResult := takt.ExecExpr[*testDispatcher, int](dExec, echoChain(depth))

		// Mode 2: Step + Advance stepping loop.
		dStep := &testDispatcher{value: 7}
		stepResult, susp := takt.Step[int](echoChain(depth))
		var sErr error
		for susp != nil {
			stepResult, susp, sErr = takt.Advance(dStep, susp)
			if sErr != nil {
				return false
			}
		}

		// Mode 3: Loop.Submit + Loop.Run via the immediateBackend.
		dLoop := &testDispatcher{value: 7}
		bLoop := &immediateBackend{dispatch: dLoop.Dispatch}
		l := takt.NewLoop[*immediateBackend, int](bLoop, 1024, takt.WithMaxCompletions(depth+4))
		if _, _, err := l.SubmitExpr(echoChain(depth)); err != nil {
			return false
		}
		runResults, runErr := l.Run()
		if runErr != nil || len(runResults) != 1 {
			return false
		}

		return execResult == stepResult && stepResult == runResults[0]
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 64}); err != nil {
		t.Error(err)
	}
}

func TestPropertyFaultTolerance(t *testing.T) {
	f := func(size, failure uint8) bool {
		count := int(size) + 1
		failAt := int(failure)%count + 1
		backend := &failSubmitBackend{dispatch: (&testDispatcher{value: 7}).Dispatch, failAt: failAt}
		l := takt.NewLoop[*failSubmitBackend, int](backend, count, takt.WithMaxCompletions(count))
		for range count {
			if _, done, err := l.SubmitExpr(propCont()); err != nil || done {
				return false
			}
		}
		_, err := l.Poll()
		return err != nil && l.Failed() == err && l.Pending() == 0 && backend.submits == failAt
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}
