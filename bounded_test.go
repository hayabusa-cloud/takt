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

type numberedOp struct{ id int }

func (numberedOp) OpResult() int { return 0 }

// The inverse of the index multiplier produces a low product and therefore
// bucket zero for these bounded test sequences. Identity equality stays exact.
func collisionToken(n uint64) takt.Token { return takt.Token(n * 0xf1de83e19937733d) }

// A bounded semantic provider: no kernel behavior is claimed by these tests.
type boundedBackend struct {
	items                    [128]takt.Completion
	order                    [512]int
	n, offers, polls, refuse int
	moreAt                   int
	terminalBlock            bool
}

func (b *boundedBackend) Submit(op kont.Operation) (takt.Token, error) {
	id := op.(numberedOp).id
	b.order[b.offers%len(b.order)] = id
	b.offers++
	if b.refuse != 0 {
		if b.refuse > 0 {
			b.refuse--
		}
		return 0, iox.ErrWouldBlock
	}
	token := collisionToken(uint64(b.offers))
	err := error(nil)
	if b.terminalBlock {
		err = iox.ErrWouldBlock
	}
	if b.moreAt != 0 && b.offers == b.moreAt {
		err = iox.ErrMore
	}
	b.items[b.n] = takt.Completion{Token: token, Value: id, Err: err}
	b.n++
	return token, nil
}
func (b *boundedBackend) Poll(dst []takt.Completion) (int, error) {
	b.polls++
	n := copy(dst, b.items[:b.n])
	copy(b.items[:], b.items[n:b.n])
	clear(b.items[b.n-n : b.n])
	b.n -= n
	return n, nil
}

func TestCapacityRefusalDoesNotEvaluateOrConsume(t *testing.T) {
	b := &boundedBackend{refuse: -1}
	l := takt.NewLoop[*boundedBackend, int](b, 1, takt.WithMaxCompletions(1))
	defer l.Drain()
	_, s := takt.Step(kont.ExprPerform(numberedOp{1}))
	if err := l.SubmitSuspension(s); err != nil {
		t.Fatal(err)
	}
	evaluations := 0
	m := kont.Eff[int](func(k func(int) kont.Resumed) kont.Resumed { evaluations++; return k(7) })
	if _, _, err := l.Submit(m); err != iox.ErrWouldBlock {
		t.Fatal(err)
	}
	if evaluations != 0 || b.offers != 0 {
		t.Fatal("capacity refusal evaluated or offered work")
	}
	if _, _, err := l.SubmitExpr(kont.ExprReturn(9)); err != iox.ErrWouldBlock {
		t.Fatal(err)
	}
	_, other := takt.Step(kont.ExprPerform(numberedOp{2}))
	if err := l.SubmitSuspension(other); err != iox.ErrWouldBlock {
		t.Fatal(err)
	}
	value, next := other.Resume(2)
	if value != 2 || next != nil {
		t.Fatal("refused suspension was consumed")
	}
	l.Drain()
	if _, _, err := l.Submit(m); err != takt.ErrDisposed || evaluations != 0 {
		t.Fatal("disposed submit evaluated prefix", err)
	}
	_, after := takt.Step(kont.ExprPerform(numberedOp{3}))
	if err := l.SubmitSuspension(after); err != takt.ErrDisposed {
		t.Fatal(err)
	}
	after.Discard()
}

func TestRefusalRetainsFIFOAndServicesBackend(t *testing.T) {
	b := &boundedBackend{refuse: -1}
	l := takt.NewLoop[*boundedBackend, int](b, 5, takt.WithMaxCompletions(2))
	defer l.Drain()
	for i := range 5 {
		l.SubmitExpr(kont.ExprPerform(numberedOp{i}))
	}
	for range 3 {
		if _, err := l.Poll(); err != nil {
			t.Fatal(err)
		}
	}
	want := [6]int{0, 1, 2, 3, 4, 0}
	for i, v := range want {
		if b.order[i] != v {
			t.Fatalf("order[%d]=%d want %d", i, b.order[i], v)
		}
	}
	if b.polls != 3 || l.Pending() != 5 || l.Ready() {
		t.Fatal("refusal became runnable or lost state/service")
	}
	b.refuse = 0
	values, err := l.Run()
	if err != nil || len(values) != 5 {
		t.Fatalf("run: %v %v", values, err)
	}
}

func TestTerminalWouldBlockIsDeferred(t *testing.T) {
	b := &boundedBackend{terminalBlock: true}
	l := takt.NewLoop[*boundedBackend, int](b, 1, takt.WithMaxCompletions(4))
	defer l.Drain()
	l.SubmitExpr(kont.ExprPerform(numberedOp{7}))
	for round := range 5 {
		values, err := l.Poll()
		if err != nil || values != nil || l.Ready() || l.Pending() != 1 || b.offers != round+1 {
			t.Fatalf("retry churn reported runnable progress: round=%d values=%v err=%v", round, values, err)
		}
	}
	b.terminalBlock = false
	values, err := l.Run()
	if err != nil || len(values) != 1 || values[0] != 7 {
		t.Fatalf("%v %v", values, err)
	}
}

func TestSuccessorUsesSameSlotInLaterTick(t *testing.T) {
	b := &boundedBackend{}
	l := takt.NewLoop[*boundedBackend, int](b, 1, takt.WithMaxCompletions(8))
	defer l.Drain()
	resumes := 0
	m := kont.ExprBind(kont.ExprPerform(numberedOp{1}), func(v int) kont.Expr[int] {
		resumes++
		return kont.ExprPerform(numberedOp{v + 1})
	})
	l.SubmitExpr(m)
	values, err := l.Poll()
	if err != nil || len(values) != 0 || resumes != 1 || b.offers != 1 || !l.Ready() || l.Pending() != 1 {
		t.Fatal("successor lost, replayed, or offered in same tick")
	}
	values, err = l.Poll()
	if err != nil || len(values) != 1 || values[0] != 2 || l.Pending() != 0 {
		t.Fatalf("%v %v", values, err)
	}
}

func TestBatchAccountedBeforePrefixResumes(t *testing.T) {
	b := &boundedBackend{moreAt: 2}
	l := takt.NewLoop[*boundedBackend, int](b, 3, takt.WithMaxCompletions(3))
	defer l.Drain()
	first := kont.ExprBind(kont.ExprPerform(numberedOp{1}), func(v int) kont.Expr[int] {
		if !errors.Is(l.Failed(), takt.ErrUnsupportedMultishot) {
			t.Error("prefix resumed before later batch fault was accounted")
		}
		return kont.ExprReturn(v)
	})
	l.SubmitExpr(first)
	l.SubmitExpr(kont.ExprPerform(numberedOp{2}))
	lateRan := false
	l.SubmitExpr(kont.ExprBind(kont.ExprPerform(numberedOp{3}), func(v int) kont.Expr[int] { lateRan = true; return kont.ExprReturn(v) }))
	values, err := l.Poll()
	if err != nil || len(values) != 1 || values[0] != 1 || lateRan || l.Pending() != 0 {
		t.Fatalf("fatal prefix: %v %v late=%v", values, err, lateRan)
	}
	if _, err = l.Poll(); err != takt.ErrUnsupportedMultishot {
		t.Fatal(err)
	}
}

func TestFirstUseBookkeepingAllocationsAndCollisions(t *testing.T) {
	const runs = 16
	var loops [runs + 1]*takt.Loop[*boundedBackend, int]
	var susp [runs + 1][64]*kont.Suspension[int]
	for i := range loops {
		loops[i] = takt.NewLoop[*boundedBackend, int](&boundedBackend{}, 64, takt.WithMaxCompletions(64))
		defer loops[i].Drain()
		for j := range susp[i] {
			_, susp[i][j] = takt.Step(kont.ExprPerform(numberedOp{j}))
		}
	}
	i := 0
	allocs := testing.AllocsPerRun(runs, func() {
		l := loops[i]
		for _, s := range susp[i] {
			if err := l.SubmitSuspension(s); err != nil {
				panic(err)
			}
		}
		values, err := l.Poll()
		if err != nil || len(values) != 64 {
			panic("lost colliding completion")
		}
		i++
	})
	if allocs != 0 {
		t.Fatalf("fresh provisioned loop: %g allocations", allocs)
	}
}

func TestPositiveRunnerCapacity(t *testing.T) {
	for _, n := range []int{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("accepted nonpositive capacity")
				}
			}()
			takt.NewLoop[*boundedBackend, int](&boundedBackend{}, n)
		}()
	}
}

func TestRunFatalPrefixDisposesSuccessor(t *testing.T) {
	b := &boundedBackend{moreAt: 3}
	l := takt.NewLoop[*boundedBackend, int](b, 3, takt.WithMaxCompletions(3))
	defer l.Drain()
	l.SubmitExpr(kont.ExprPerform(numberedOp{1}))
	l.SubmitExpr(kont.ExprBind(kont.ExprPerform(numberedOp{2}), func(v int) kont.Expr[int] { return kont.ExprPerform(numberedOp{9}) }))
	l.SubmitExpr(kont.ExprPerform(numberedOp{3}))
	values, err := l.Run()
	if err != takt.ErrUnsupportedMultishot || len(values) != 1 || values[0] != 1 || b.offers != 3 || l.Pending() != 0 {
		t.Fatalf("%v %v offers=%d pending=%d", values, err, b.offers, l.Pending())
	}
}
