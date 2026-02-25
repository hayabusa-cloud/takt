// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/kont"
)

// Reify converts Cont-world to Expr-world.
func Reify[A any](m kont.Eff[A]) kont.Expr[A] {
	return kont.Reify(m)
}

// Reflect converts Expr-world to Cont-world.
func Reflect[A any](m kont.Expr[A]) kont.Eff[A] {
	return kont.Reflect(m)
}
