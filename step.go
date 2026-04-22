// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

// AdvanceSuspension and Advance expose the package's manual stepping API.
// [Exec] blocks until completion, while [Loop] drives the same suspension
// sequence through submit/poll calls.

import (
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

// SuspensionLike is any resumable value that exposes one pending operation and
// can accept the corresponding resumption while preserving its outer type.
// Packages such as `cove` can use [AdvanceSuspension] by providing `Op` and
// `Resume` methods without requiring extra adapters in `takt`.
type SuspensionLike[S any, R any] interface {
	Op() kont.Operation
	Resume(value kont.Resumed) (R, S)
}

// Step evaluates until the first effect suspension.
// Prefer [AdvanceSuspension] or [Advance] once a suspension has already been observed.
func Step[R any](m kont.Expr[R]) (R, *kont.Suspension[R]) {
	return kont.StepExpr(m)
}

// AdvanceSuspension dispatches one suspended operation through a Dispatcher.
// Progress resumes the suspension; would-block and failure return the original
// value unchanged.
func AdvanceSuspension[D Dispatcher[D], S SuspensionLike[S, R], R any](d D, susp S) (R, S, error) {
	v, err := d.Dispatch(susp.Op())
	if iox.IsProgress(err) {
		result, next := susp.Resume(v)
		return result, next, err
	}
	var zero R
	return zero, susp, err
}

// Advance dispatches one suspended operation via a Dispatcher.
// Use [AdvanceSuspension] when the suspension also carries external context.
// Progress resumes the suspension; would-block and failure return it unchanged.
func Advance[D Dispatcher[D], R any](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error) {
	return AdvanceSuspension[D](d, susp)
}
