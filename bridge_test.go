// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"testing"

	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

func TestReifyContToExpr(t *testing.T) {
	d := &testDispatcher{value: 42}
	cont := kont.Bind(kont.Perform(echoOp{}), func(n int) kont.Eff[int] {
		return kont.Pure(n + 1)
	})
	expr := takt.Reify(cont)
	result := takt.ExecExpr(d, expr)
	if result != 43 {
		t.Fatalf("got %d, want 43", result)
	}
}

func TestReflectExprToCont(t *testing.T) {
	d := &testDispatcher{value: 42}
	expr := kont.ExprBind(kont.ExprPerform(echoOp{}), func(n int) kont.Expr[int] {
		return kont.ExprReturn[int](n + 1)
	})
	cont := takt.Reflect(expr)
	result := takt.Exec(d, cont)
	if result != 43 {
		t.Fatalf("got %d, want 43", result)
	}
}

func TestRoundTripReifyReflect(t *testing.T) {
	d := &testDispatcher{value: 10}
	cont := kont.Bind(kont.Perform(echoOp{}), func(a int) kont.Eff[int] {
		return kont.Bind(kont.Perform(echoOp{}), func(b int) kont.Eff[int] {
			return kont.Pure(a + b)
		})
	})
	roundTripped := takt.Reflect(takt.Reify(cont))
	result := takt.Exec(d, roundTripped)
	if result != 20 {
		t.Fatalf("got %d, want 20", result)
	}
}

func TestRoundTripReflectReify(t *testing.T) {
	d := &testDispatcher{value: 10}
	expr := kont.ExprBind(kont.ExprPerform(echoOp{}), func(a int) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(echoOp{}), func(b int) kont.Expr[int] {
			return kont.ExprReturn[int](a + b)
		})
	})
	roundTripped := takt.Reify(takt.Reflect(expr))
	result := takt.ExecExpr(d, roundTripped)
	if result != 20 {
		t.Fatalf("got %d, want 20", result)
	}
}
