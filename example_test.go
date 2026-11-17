// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"fmt"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

type add struct {
	kont.Phantom[int]
	a, b int
}

type arithmetic struct{}

func (arithmetic) Dispatch(op kont.Operation) (kont.Resumed, error) {
	a := op.(add)
	return a.a + a.b, nil
}

func ExampleAdvance() {
	program := kont.ExprBind(kont.ExprPerform(add{a: 1, b: 2}), func(sum int) kont.Expr[int] {
		return kont.ExprPerform(add{a: sum, b: 4})
	})
	_, susp := takt.Step(program)
	value, susp, err := takt.Advance(arithmetic{}, susp)
	fmt.Println("first dispatch:", value, susp == nil, err)
	// A nil error can leave a successor. Advance the returned suspension.
	value, susp, err = takt.Advance(arithmetic{}, susp)
	fmt.Println("second dispatch:", value, susp == nil, err)
	// Output:
	// first dispatch: 0 false <nil>
	// second dispatch: 7 true <nil>
}

// queuedArithmetic is a bounded provider for arithmetic replies. It performs no I/O.
type queuedArithmetic struct {
	token takt.Token
	reply takt.Completion
	ready bool
}

func (b *queuedArithmetic) Submit(op kont.Operation) (takt.Token, error) {
	if b.ready {
		return 0, iox.ErrWouldBlock
	}
	b.token++
	value, err := (arithmetic{}).Dispatch(op)
	b.reply = takt.Completion{Token: b.token, Value: value, Err: err}
	b.ready = true
	return b.token, nil
}

func (b *queuedArithmetic) Poll(out []takt.Completion) (int, error) {
	if !b.ready {
		return 0, nil
	}
	out[0] = b.reply
	b.reply = takt.Completion{}
	b.ready = false
	return 1, nil
}

func ExampleLoop() {
	backend := &queuedArithmetic{}
	loop := takt.NewLoop[*queuedArithmetic, int](backend, 1, takt.WithMaxCompletions(1))
	defer loop.Drain() // returns runner storage; native bindings need their own settlement
	for _, program := range []kont.Expr[int]{kont.ExprReturn(3), kont.ExprPerform(add{a: 3, b: 4})} {
		value, done, err := loop.SubmitExpr(program)
		if err != nil {
			panic(err)
		}
		if done {
			fmt.Println("immediate:", value)
			continue
		}
		fmt.Println("admitted:", loop.Pending(), "backend accepted:", backend.ready)
		values, err := loop.Run()
		fmt.Println("completed:", values, err)
	}
	// Output:
	// immediate: 3
	// admitted: 1 backend accepted: false
	// completed: [7] <nil>
}
