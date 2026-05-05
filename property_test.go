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
		loop := takt.NewLoop[*propBackend, int](backend, takt.WithMaxCompletions(count))

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
		l := takt.NewLoop[*immediateBackend, int](bLoop, takt.WithMaxCompletions(depth+4))
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
	f := func(count int, failIdx int) bool {
		if count < 0 {
			count = -count
		}
		count = (count % 100) + 2 // 2 to 102 items

		if failIdx < 0 {
			failIdx = -failIdx
		}
		failIdx = failIdx % count

		backend := &propBackend{nextT: 1, shuffle: false}
		loop := takt.NewLoop[*propBackend, int](backend, takt.WithMaxCompletions(count))

		successCount := 0
		for i := range count {
			if i == failIdx {
				backend.failSubmit = true
			} else {
				backend.failSubmit = false
			}
			_, _, err := loop.SubmitExpr(propCont())
			if err == nil {
				successCount++
			}
		}

		var results []int
		for len(results) < successCount {
			res, _ := loop.Poll()
			results = append(results, res...)
		}

		if len(results) != successCount {
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}
