// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"errors"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

// Dispatcher is the F-bounded interface for non-blocking operation dispatch.
// Dispatch returns (value, nil) on completion, (value, iox.ErrMore) when
// progress is made and more completions remain, (nil, iox.ErrWouldBlock) when
// no progress is currently possible, or (nil, error) on infrastructure failure.
type Dispatcher[D Dispatcher[D]] interface {
	Dispatch(op kont.Operation) (kont.Resumed, error)
}

// ErrUnsupportedMultishot reports that a multishot completion resumed into a
// new suspended effect that the current Loop token model cannot safely track.
var ErrUnsupportedMultishot = errors.New("takt: multishot completion cannot suspend on a new effect")

// dispatchFailed panics on infrastructure failure.
// noinline keeps Dispatch methods inlineable.
//
//go:noinline
func dispatchFailed(err error) {
	panic("takt: dispatch failed: " + err.Error())
}

// handler adapts a Dispatcher as kont.Handler.
// Waits on ErrWouldBlock with adaptive backoff. Value type for stack allocation.
type handler[D Dispatcher[D], R any] struct {
	d D
}

// Dispatch delegates to dispatchWait.
func (h handler[D, R]) Dispatch(op kont.Operation) (kont.Resumed, bool) {
	return dispatchWait(h.d, op), true
}

// dispatchWait loops until Dispatch succeeds.
// IsProgress → return, WouldBlock → adaptive backoff, failure → panic.
func dispatchWait[D Dispatcher[D]](d D, op kont.Operation) kont.Resumed {
	var bo iox.Backoff
	for {
		v, err := d.Dispatch(op)
		if iox.IsProgress(err) {
			return v
		}
		if !iox.IsWouldBlock(err) {
			dispatchFailed(err)
		}
		bo.Wait()
	}
}

// Exec runs a Cont-world computation to completion via a Dispatcher.
func Exec[D Dispatcher[D], R any](d D, m kont.Eff[R]) R {
	return kont.Handle(m, handler[D, R]{d: d})
}

// ExecExpr runs an Expr-world computation to completion via a Dispatcher.
func ExecExpr[D Dispatcher[D], R any](d D, m kont.Expr[R]) R {
	return kont.HandleExpr(m, handler[D, R]{d: d})
}
