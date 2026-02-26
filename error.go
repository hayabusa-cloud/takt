// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

// dispatcherErrorHandler handles Error and Dispatcher effects.
// Dispatch order: Error → Dispatcher. Value type for stack allocation.
type dispatcherErrorHandler[D Dispatcher[D], E, A any] struct {
	d      D
	errCtx *kont.ErrorContext[E]
}

// Dispatch implements kont.Handler. Error ops first, then Dispatcher.
func (h dispatcherErrorHandler[D, E, A]) Dispatch(op kont.Operation) (kont.Resumed, bool) {
	// Error ops: eager dispatch
	if eop, ok := op.(interface {
		DispatchError(ctx *kont.ErrorContext[E]) (kont.Resumed, bool)
	}); ok {
		v, _ := eop.DispatchError(h.errCtx)
		if h.errCtx.HasErr {
			return kont.Left[E, A](h.errCtx.Err), false
		}
		return v, true
	}
	// Dispatcher ops: wait with full iox classification
	return dispatchWait(h.d, op), true
}

// ExecError runs a Cont-world computation with error handling.
// Returns Right on success, Left on Throw.
func ExecError[E any, D Dispatcher[D], R any](d D, m kont.Eff[R]) kont.Either[E, R] {
	wrapped := kont.Map[kont.Resumed, R, kont.Either[E, R]](m, func(r R) kont.Either[E, R] {
		return kont.Right[E, R](r)
	})
	var ctx kont.ErrorContext[E]
	h := dispatcherErrorHandler[D, E, R]{d: d, errCtx: &ctx}
	return kont.Handle(wrapped, h)
}

// ExecErrorExpr is the Expr-world ExecError.
// Blocks on iox.ErrWouldBlock via adaptive backoff (iox.Backoff).
func ExecErrorExpr[E any, D Dispatcher[D], R any](d D, m kont.Expr[R]) kont.Either[E, R] {
	wrapped := wrapRight[E, R](m)
	var ctx kont.ErrorContext[E]
	h := dispatcherErrorHandler[D, E, R]{d: d, errCtx: &ctx}
	return kont.HandleExpr(wrapped, h)
}

func rightUnwind[E, R any](_, _, _, current kont.Erased) (kont.Erased, kont.Frame) {
	return kont.Erased(kont.Right[E, R](current.(R))), kont.ReturnFrame{}
}

// wrapRight wraps a protocol Expr[R] into Expr[Either[E, R]] using a pooled UnwindFrame.
func wrapRight[E, R any](protocol kont.Expr[R]) kont.Expr[kont.Either[E, R]] {
	if _, ok := protocol.Frame.(kont.ReturnFrame); ok {
		return kont.ExprReturn(kont.Right[E, R](protocol.Value))
	}
	uf := kont.AcquireUnwindFrame()
	uf.Unwind = rightUnwind[E, R]
	return kont.Expr[kont.Either[E, R]]{Frame: kont.ChainFrames(protocol.Frame, uf)}
}

// StepError evaluates a computation with error support until the first suspension.
// Returns (Either[E, R], nil) on completion or error, or (zero, suspension) if pending.
func StepError[E, R any](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]]) {
	return kont.StepExpr(wrapRight[E, R](m))
}

// AdvanceError dispatches the suspended operation.
// Error ops: eager (Throw → Discard + Left). Dispatcher ops: full iox classification.
func AdvanceError[E any, D Dispatcher[D], R any](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error) {
	// Error ops: eager dispatch
	if eop, ok := susp.Op().(interface {
		DispatchError(ctx *kont.ErrorContext[E]) (kont.Resumed, bool)
	}); ok {
		var ctx kont.ErrorContext[E]
		v, _ := eop.DispatchError(&ctx)
		if ctx.HasErr {
			susp.Discard()
			return kont.Left[E, R](ctx.Err), nil, nil
		}
		result, next := susp.Resume(v)
		return result, next, nil
	}
	// Dispatcher ops: full iox classification
	v, err := d.Dispatch(susp.Op())
	if iox.IsProgress(err) {
		result, next := susp.Resume(v)
		return result, next, err
	}
	var zero kont.Either[E, R]
	return zero, susp, err
}
