// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

// Package takt provides the abstract proactor execution model via algebraic
// effects on [code.hybscloud.com/kont].
//
// takt dispatches effect operations through an F-bounded [Dispatcher] with
// full iox outcome classification.
//
// # Execution Strategies
//
//   - Blocking: [Exec]/[ExecExpr] wait on ErrWouldBlock
//   - Stepping: [Step] and [Advance] evaluate one effect at a time for proactor integration
//   - Event loop: [Loop] drives computations through a [Backend] (submit/poll)
//
// # iox Classification
//
//   - nil: completed
//   - [code.hybscloud.com/iox.ErrMore]: progress, more completions expected
//   - [code.hybscloud.com/iox.ErrWouldBlock]: no progress, retry later
//   - failure: infrastructure error
//
// Event-loop backends surface infrastructure poll failures directly from
// [Backend.Poll], while [Loop.Poll] / [Loop.Run] still treat
// [code.hybscloud.com/iox.ErrWouldBlock] as idle. [Loop.Poll] / [Loop.Run]
// return [ErrUnsupportedMultishot] when an
// [code.hybscloud.com/iox.ErrMore] completion would otherwise resume into a new
// suspended effect that has no backend submission of its own.
//
// # Error Handling
//
// [ExecError]/[ExecErrorExpr]/[StepError]/[AdvanceError] compose Dispatcher
// and kont Error effects. Dispatch order: Error → Dispatcher. Results are
// [code.hybscloud.com/kont.Either] — Right on success, Left on Throw.
//
// # Bridge
//
//   - [Reify]: Cont → Expr
//   - [Reflect]: Expr → Cont
package takt
