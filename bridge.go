// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/kont"
)

// Reify runs m to its first return or operation and returns a [kont.Expr].
// A completed result is reusable. A pending result and its copies share one
// invocation. Stepping it a second time panics before yielding the operation
// again. Reify a reusable m again for each run.
func Reify[A any](m kont.Eff[A]) kont.Expr[A] {
	return kont.Reify(m)
}

// Reflect converts [kont.Expr] to [kont.Eff]. The returned computation is
// reusable when m is reusable. A pending [Reify] result and its reflection
// share one invocation.
func Reflect[A any](m kont.Expr[A]) kont.Eff[A] {
	return kont.Reflect(m)
}
