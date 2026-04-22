// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

// dispatcherErrorHandler handles Error and Dispatcher operations.
// Error operations run before dispatcher operations. Value type for stack allocation.
type dispatcherErrorHandler[D Dispatcher[D], E, A any] struct {
	d      D
	errCtx *kont.ErrorContext[E]
}

// Dispatch implements kont.Handler. Error operations run first.
func (h dispatcherErrorHandler[D, E, A]) Dispatch(op kont.Operation) (kont.Resumed, bool) {
	// Error operations are handled eagerly.
	if eop, ok := op.(interface {
		DispatchError(ctx *kont.ErrorContext[E]) (kont.Resumed, bool)
	}); ok {
		v, _ := eop.DispatchError(h.errCtx)
		if h.errCtx.HasErr {
			return kont.Left[E, A](h.errCtx.Err), false
		}
		return v, true
	}
	// Non-error operations go through the dispatcher and wait as needed.
	return dispatchWait(h.d, op), true
}

// ExecError runs a [kont.Eff] computation with error handling.
// It returns Right on success and Left on Throw.
func ExecError[E any, D Dispatcher[D], R any](d D, m kont.Eff[R]) kont.Either[E, R] {
	wrapped := kont.Map[kont.Resumed, R, kont.Either[E, R]](m, func(r R) kont.Either[E, R] {
		return kont.Right[E, R](r)
	})
	var ctx kont.ErrorContext[E]
	h := dispatcherErrorHandler[D, E, R]{d: d, errCtx: &ctx}
	return kont.Handle(wrapped, h)
}

// ExecErrorExpr is the [kont.Expr] form of [ExecError].
// It waits on [iox.ErrWouldBlock] via adaptive backoff.
func ExecErrorExpr[E any, D Dispatcher[D], R any](d D, m kont.Expr[R]) kont.Either[E, R] {
	wrapped := wrapRight[E, R](m)
	var ctx kont.ErrorContext[E]
	h := dispatcherErrorHandler[D, E, R]{d: d, errCtx: &ctx}
	return kont.HandleExpr(wrapped, h)
}

func rightUnwind[E, R any](_, _, _, current kont.Erased) (kont.Erased, kont.Frame) {
	return kont.Erased(kont.Right[E, R](current.(R))), kont.ReturnFrame{}
}

// wrapRight converts an Expr[R] into Expr[Either[E, R]] using a pooled UnwindFrame.
func wrapRight[E, R any](protocol kont.Expr[R]) kont.Expr[kont.Either[E, R]] {
	if _, ok := protocol.Frame.(kont.ReturnFrame); ok {
		return kont.ExprReturn(kont.Right[E, R](protocol.Value))
	}
	uf := kont.AcquireUnwindFrame()
	uf.Unwind = rightUnwind[E, R]
	return kont.Expr[kont.Either[E, R]]{Frame: kont.ChainFrames(protocol.Frame, uf)}
}

// StepError evaluates a computation with error support until the first suspension.
// It returns (Either[E, R], nil) on completion or error, or (zero, suspension) if pending.
func StepError[E, R any](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]]) {
	return kont.StepExpr(wrapRight[E, R](m))
}

// AdvanceError dispatches the suspended operation.
// Throw discards the suspension and returns Left; other operations go through
// the dispatcher with the usual `iox` classification.
func AdvanceError[E any, D Dispatcher[D], R any](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error) {
	// Error operations are handled eagerly.
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
	// Other operations go through the dispatcher.
	v, err := d.Dispatch(susp.Op())
	if iox.IsProgress(err) {
		result, next := susp.Resume(v)
		return result, next, err
	}
	var zero kont.Either[E, R]
	return zero, susp, err
}
