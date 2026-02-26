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

func (b *benchBackend) Poll(completions []takt.Completion) int {
	if len(completions) > 0 {
		completions[0] = takt.Completion{
			Token: 1,
			Value: b.value,
			Err:   nil,
		}
		return 1
	}
	return 0
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
	for i := 0; i < b.N; i++ {
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
	for i := 0; i < b.N; i++ {
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

func (b *dummyRaceBackend) Poll(completions []takt.Completion) int {
	return 0
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

func BenchmarkStepErrorPure(b *testing.B) {
	expr := kont.ExprReturn(42)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, _ := takt.StepError[string, int](expr)
		if !res.IsRight() {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkStepErrorBindChain(b *testing.B) {
	var expr kont.Expr[int] = kont.ExprReturn(0)
	for i := 0; i < 10; i++ {
		expr = kont.ExprBind(expr, func(v int) kont.Expr[int] {
			return kont.ExprReturn(v + 1)
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, _ := takt.StepError[string, int](expr)
		if !res.IsRight() {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkExecErrorExpr(b *testing.B) {
	d := &testDispatcher{value: 42}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		expr := kont.ExprBind(kont.ExprPerform(echoOp{}), func(n int) kont.Expr[int] {
			return kont.ExprReturn(1)
		})
		res := takt.ExecErrorExpr[string](d, expr)
		if !res.IsRight() {
			b.Fatal("unexpected result")
		}
	}
}
