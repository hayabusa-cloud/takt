// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"errors"
	"fmt"
	"testing"

	"code.hybscloud.com/cove"
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

type classificationDispatcher struct {
	control error
	calls   int
}

func (d *classificationDispatcher) Dispatch(kont.Operation) (kont.Resumed, error) {
	d.calls++
	if d.calls == 1 {
		return -1, d.control // not a reply when Classify reports WouldBlock
	}
	return 42, nil
}

func TestDispatcherWouldBlockClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		control error
	}{
		{"wrapped", fmt.Errorf("pending: %w", iox.ErrWouldBlock)},
		{"joined-block-more", errors.Join(iox.ErrWouldBlock, iox.ErrMore)},
		{"joined-more-block", errors.Join(iox.ErrMore, iox.ErrWouldBlock)},
		{"nested", fmt.Errorf("pending: %w", errors.Join(iox.ErrMore, iox.ErrWouldBlock))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if iox.Classify(tc.control) != iox.OutcomeWouldBlock {
				t.Fatal("fixture must classify as WouldBlock")
			}
			t.Run("Advance", func(t *testing.T) {
				d := &classificationDispatcher{control: tc.control}
				_, s := takt.Step(kont.ExprPerform(echoOp{}))
				defer s.Discard()
				value, retry, err := takt.Advance(d, s)
				if value != 0 || retry != s || err != tc.control {
					t.Fatalf("would-block consumed capture or changed error: %v %v %v", value, retry == s, err)
				}
				value, next, err := takt.Advance(d, retry)
				if err != nil || next != nil || value != 42 || d.calls != 2 {
					t.Fatalf("retained capture did not receive reply: %v %v", value, err)
				}
			})
			t.Run("contextual", func(t *testing.T) {
				d := &classificationDispatcher{control: tc.control}
				_, s := cove.StepExprWith("context", kont.ExprPerform(echoOp{}))
				defer s.Suspension.Discard()
				value, retry, err := takt.AdvanceSuspension(d, s)
				if value != 0 || retry.Suspension != s.Suspension || retry.Ask() != s.Ask() || err != tc.control {
					t.Fatal("would-block changed the contextual frontier")
				}
				value, next, err := takt.AdvanceSuspension(d, retry)
				if err != nil || next.Suspension != nil || next.Ask() != s.Ask() || value != 42 || d.calls != 2 {
					t.Fatal("retained contextual frontier did not receive reply")
				}
			})
			t.Run("AdvanceError", func(t *testing.T) {
				d := &classificationDispatcher{control: tc.control}
				_, s := takt.StepError[string](kont.ExprPerform(echoOp{}))
				defer s.Discard()
				_, retry, err := takt.AdvanceError(d, s)
				if retry != s || err != tc.control {
					t.Fatal("would-block consumed error-aware frontier")
				}
				result, next, err := takt.AdvanceError(d, retry)
				value, ok := result.GetRight()
				if err != nil || next != nil || !ok || value != 42 || d.calls != 2 {
					t.Fatal("retained error-aware frontier did not receive reply")
				}
			})
			for _, mode := range []struct {
				name string
				run  func(*classificationDispatcher) int
			}{
				{"Exec", func(d *classificationDispatcher) int { return takt.Exec(d, kont.Perform(echoOp{})) }},
				{"ExecExpr", func(d *classificationDispatcher) int { return takt.ExecExpr(d, kont.ExprPerform(echoOp{})) }},
				{"ExecError", func(d *classificationDispatcher) int {
					v, _ := takt.ExecError[string](d, kont.Perform(echoOp{})).GetRight()
					return v
				}},
				{"ExecErrorExpr", func(d *classificationDispatcher) int {
					v, _ := takt.ExecErrorExpr[string](d, kont.ExprPerform(echoOp{})).GetRight()
					return v
				}},
			} {
				t.Run(mode.name, func(t *testing.T) {
					d := &classificationDispatcher{control: tc.control}
					if value := mode.run(d); value != 42 || d.calls != 2 {
						t.Fatalf("delivered a would-block value: got %d after %d calls", value, d.calls)
					}
				})
			}
		})
	}
}

func TestAdvanceWrappedMorePreservesError(t *testing.T) {
	errMore := fmt.Errorf("live: %w", iox.ErrMore)
	d := &classificationDispatcher{control: errMore}
	_, s := takt.Step(kont.ExprPerform(echoOp{}))
	defer s.Discard()
	value, next, err := takt.Advance(d, s)
	if err != errMore || next != nil || value != -1 || d.calls != 1 {
		t.Fatalf("wrapped More lost its reply or identity: %v %v", value, err)
	}
}

func TestFirstUseDispatchClassificationAllocations(t *testing.T) {
	for _, control := range []error{nil, iox.ErrWouldBlock, iox.ErrMore,
		errors.New("provider failed"), fmt.Errorf("pending: %w", iox.ErrWouldBlock),
		fmt.Errorf("live: %w", iox.ErrMore), errors.Join(iox.ErrMore, iox.ErrWouldBlock)} {
		const runs = 16
		var dispatchers [runs + 1]performanceControlDispatcher
		for i := range dispatchers {
			dispatchers[i].control = control
		}
		i := 0
		allocs := testing.AllocsPerRun(runs, func() {
			// The warmup and every measured call receive a fresh dispatcher.
			value, _, err := takt.AdvanceSuspension(&dispatchers[i], performanceCursor(0))
			if err != control {
				panic("changed error identity")
			}
			performanceResult = value
			i++
		})
		if allocs != 0 {
			t.Fatalf("classification %v: %g allocations", control, allocs)
		}
	}
}
