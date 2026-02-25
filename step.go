// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

// Step evaluates until the first effect suspension.
func Step[R any](m kont.Expr[R]) (R, *kont.Suspension[R]) {
	return kont.StepExpr(m)
}

// Advance dispatches one suspended operation via a Dispatcher.
// IsProgress: suspension consumed. WouldBlock/failure: unconsumed.
func Advance[D Dispatcher[D], R any](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error) {
	v, err := d.Dispatch(susp.Op())
	if iox.IsProgress(err) {
		result, next := susp.Resume(v)
		return result, next, err
	}
	var zero R
	return zero, susp, err
}
