// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
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
	loop := takt.NewLoop[*benchBackend, int](backend, 1024, takt.WithMaxCompletions(1))
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
	loop := takt.NewLoop[*benchBackend, int](backend, 1024, takt.WithMaxCompletions(1))
	expr := kont.ExprReturn(42)

	b.ResetTimer()
	for b.Loop() {
		res, done, _ := loop.SubmitExpr(expr)
		if !done || res != 42 {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkLoopBoundedRound(b *testing.B) {
	l := takt.NewLoop[*benchBackend, int](&benchBackend{value: 42}, 1, takt.WithMaxCompletions(1))
	defer l.Drain()
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := l.SubmitExpr(kont.ExprPerform(echoOp{})); err != nil {
			b.Fatal(err)
		}
		if _, err := l.Poll(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoopRunAccumulatedResults(b *testing.B) {
	const pending = 64

	d := &testDispatcher{value: 7}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		backend := &immediateBackend{dispatch: d.Dispatch}
		loop := takt.NewLoop[*immediateBackend, int](backend, 1024, takt.WithMaxCompletions(pending))
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

func BenchmarkLoopRunChainedSingleCompletion(b *testing.B) {
	d := &testDispatcher{value: 1}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		backend := &immediateBackend{dispatch: d.Dispatch}
		loop := takt.NewLoop[*immediateBackend, int](backend, 1024, takt.WithMaxCompletions(1))
		if _, done, err := loop.SubmitExpr(echoChain(64)); err != nil || done {
			b.Fatalf("submit: done=%v err=%v", done, err)
		}
		b.StartTimer()

		results, err := loop.Run()
		if err != nil {
			b.Fatal(err)
		}
		if len(results) != 1 || results[0] != 64 {
			b.Fatalf("results = %#v, want [64]", results)
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
