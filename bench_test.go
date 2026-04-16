package takt_test

import (
	"sync/atomic"
	"testing"

	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

type benchBackend struct {
	value kont.Resumed
}

func (b *benchBackend) Submit(op kont.Operation) (takt.Token, error) {
	return 1, nil
}

func (b *benchBackend) Poll(completions []takt.Completion) (int, error) {
	if len(completions) > 0 {
		completions[0] = takt.Completion{
			Token: 1,
			Value: b.value,
			Err:   nil,
		}
		return 1, nil
	}
	return 0, nil
}

type benchOp struct{ kont.Phantom[int] }

func benchCont(val int) kont.Expr[int] {
	return kont.ExprBind(
		kont.ExprPerform(benchOp{}),
		func(v int) kont.Expr[int] {
			return kont.ExprReturn(v)
		},
	)
}

func BenchmarkLoopSubmitPoll(b *testing.B) {
	backend := &benchBackend{value: kont.Erased(42)}
	loop := takt.NewLoop[*benchBackend, int](backend, 1)
	expr := benchCont(42)

	b.ResetTimer()
	for b.Loop() {
		_, done, _ := loop.SubmitExpr(expr)
		if done {
			b.Fatal("expected pending")
		}
		res, _ := loop.Poll()
		if len(res) != 1 || res[0] != 42 {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkLoopSubmitPure(b *testing.B) {
	backend := &benchBackend{}
	loop := takt.NewLoop[*benchBackend, int](backend, 1)
	expr := kont.ExprReturn(42)

	b.ResetTimer()
	for b.Loop() {
		res, done, _ := loop.SubmitExpr(expr)
		if !done || res != 42 {
			b.Fatal("unexpected result")
		}
	}
}

type dummyRaceBackend struct {
	nextTok uint64
}

type raceOp struct{}

func (raceOp) OpResult() *int { return nil }

func (b *dummyRaceBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := atomic.AddUint64(&b.nextTok, 1)
	return takt.Token(tok), nil
}

func (b *dummyRaceBackend) Poll(completions []takt.Completion) (int, error) {
	return 0, nil
}

func BenchmarkLoopConcurrentSubmit(b *testing.B) {
	backend := &dummyRaceBackend{}
	l := takt.NewLoop[*dummyRaceBackend, *int](backend, 1024)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l.SubmitExpr(kont.ExprPerform(raceOp{}))
		}
	})
}

func BenchmarkLoopRunAccumulatedResults(b *testing.B) {
	const pending = 64

	d := &testDispatcher{value: 7}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		backend := &immediateBackend{dispatch: d.Dispatch}
		loop := takt.NewLoop[*immediateBackend, int](backend, pending)
		for j := 0; j < pending; j++ {
			if _, done, err := loop.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil || done {
				b.Fatalf("submit[%d]: done=%v err=%v", j, done, err)
			}
		}
		b.StartTimer()

		results, err := loop.Run()
		if err != nil {
			b.Fatal(err)
		}
		if len(results) != pending {
			b.Fatalf("got %d results, want %d", len(results), pending)
		}
	}
}

func BenchmarkStepErrorPure(b *testing.B) {
	expr := kont.ExprReturn(42)

	b.ResetTimer()
	for b.Loop() {
		res, _ := takt.StepError[string, int](expr)
		if !res.IsRight() {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkStepErrorBindChain(b *testing.B) {
	var expr kont.Expr[int] = kont.ExprReturn(0)
	for range 10 {
		expr = kont.ExprBind(expr, func(v int) kont.Expr[int] {
			return kont.ExprReturn(v + 1)
		})
	}

	b.ResetTimer()
	for b.Loop() {
		res, _ := takt.StepError[string, int](expr)
		if !res.IsRight() {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkExecErrorExpr(b *testing.B) {
	d := &testDispatcher{value: 42}

	b.ResetTimer()
	for b.Loop() {
		expr := kont.ExprBind(kont.ExprPerform(echoOp{}), func(n int) kont.Expr[int] {
			return kont.ExprReturn(1)
		})
		res := takt.ExecErrorExpr[string](d, expr)
		if !res.IsRight() {
			b.Fatal("unexpected result")
		}
	}
}
