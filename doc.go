// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

// Package takt provides an abstract proactor runner for
// [code.hybscloud.com/kont] computations.
//
// Responsibility remains split by layer:
//
//   - [code.hybscloud.com/iox] classifies outcome/progress evidence
//   - [code.hybscloud.com/kont] defines suspension and resumption shape
//   - [code.hybscloud.com/cove] carries explicit context across suspension boundaries
//   - takt advances and resubmits suspended computations
//
// takt dispatches suspended operations through [Dispatcher] and uses `iox`
// classification at the execution boundary instead of redefining it.
//
// # Primary Operational Surface
//
//   - Stepping: [AdvanceSuspension] and [Advance] dispatch one observed suspension at a time
//   - Event loop: [Loop] drives computations through a [Backend] (submit/poll)
//
// # Convenience Surface
//
//   - Blocking: [Exec]/[ExecExpr] wait on ErrWouldBlock with adaptive backoff
//   - Error-aware blocking: [ExecError]/[ExecErrorExpr] return [code.hybscloud.com/kont.Either]
//   - Stepping with errors: [StepError]/[AdvanceError] preserve the [code.hybscloud.com/kont.Either] result at each step
//   - Bridge helpers: [Step] reuses [code.hybscloud.com/kont.StepExpr]; [Reify] and [Reflect] re-export the `kont` conversions so callers do not need a second import
//   - Lifecycle: [Loop.Failed], [Loop.Drain], and [ErrDisposed] expose the terminal fatal state
//
// # iox Classification
//
//   - nil: completed
//   - [code.hybscloud.com/iox.ErrMore]: progress, more completions expected
//   - [code.hybscloud.com/iox.ErrWouldBlock]: no progress, retry later
//   - failure: infrastructure error
//
// Event-loop backends report infrastructure poll failures directly from
// [Backend.Poll]. [Loop.Poll] and [Loop.Run] keep poll failures separate from
// completion classification: poll-level [code.hybscloud.com/iox.ErrWouldBlock]
// is treated as idle, while completion-level
// [code.hybscloud.com/iox.ErrWouldBlock] triggers a fresh submission for the
// same suspension. [Loop.Poll] and [Loop.Run] return
// [ErrUnsupportedMultishot] when an [code.hybscloud.com/iox.ErrMore]
// completion would otherwise resume into a new suspended effect with no
// backend submission of its own.
//
// [CompletionMemory] supplies the [Completion] slice that a [Loop] passes to
// [Backend.Poll]. Use [NewLoop] with [Option]s: [WithMemory] installs a custom
// provider, [WithMaxCompletions] caps the visible slice length, [HeapMemory] is
// the default, and [BoundedMemory] provides a single bounded pool of
// default-sized 128 KiB slabs. [Loop.Drain] releases that slice exactly once
// through [CompletionMemory.Release].
//
// # Error Handling
//
// [ExecError]/[ExecErrorExpr]/[StepError]/[AdvanceError] combine [Dispatcher]
// with `kont` Error effects. Error operations run before dispatcher
// operations. Results are [code.hybscloud.com/kont.Either]: Right on success,
// Left on Throw.
//
// [code.hybscloud.com/cove.SuspensionView] already satisfies
// [SuspensionLike] through its `Op` and `Resume` methods, so it can be passed
// directly to [AdvanceSuspension].
//
// # Stepping behavior
//
// Each [AdvanceSuspension] call handles exactly one suspended operation. If
// resuming that operation produces another suspension, the caller receives it
// back and decides how to continue.
//
// # Execution styles
//
// For deterministic dispatchers, [Exec], a loop built from [Step]/[Advance],
// and [Loop.Run] produce the same externally visible results. Choose the style
// that fits your integration: blocking execution, manual stepping, or a
// backend-driven event loop.
package takt
