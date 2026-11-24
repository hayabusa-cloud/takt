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
//   - Event loop: [Loop] admits bounded computations and drives a [Backend] (submit/poll)
//   - Stream loop: [SubscriptionLoop] drives route-indexed stream observations through a [SubscriptionBackend]
//
// # Convenience Surface
//
//   - Blocking: [Exec]/[ExecExpr] wait on ErrWouldBlock with adaptive backoff
//   - Error-aware blocking: [ExecError]/[ExecErrorExpr] return [code.hybscloud.com/kont.Either]
//   - Stepping with errors: [StepError]/[AdvanceError] preserve the [code.hybscloud.com/kont.Either] result at each step
//   - Contextual stepping: [AdvanceSuspension] applies the same one-operation
//     movement law to contextual suspension carriers such as
//     [code.hybscloud.com/cove.SuspensionView] without making takt own their
//     context
//   - Bridge helpers: [Step] reuses [code.hybscloud.com/kont.StepExpr]; [Reify] and [Reflect] re-export the `kont` conversions so callers do not need a second import
//   - Lifecycle: [Loop.Failed], [Loop.Drain], [SubscriptionLoop.Failed], [SubscriptionLoop.Drain], [ErrDisposed], [ErrLiveTokenReuse], [ErrLiveRouteReuse], and [ErrUnsupportedMultishot] expose terminal fatal states
//
// # iox Classification
//
//   - nil: success at the observed boundary
//   - [code.hybscloud.com/iox.ErrMore]: the interaction remains live
//   - [code.hybscloud.com/iox.ErrWouldBlock]: further progress requires readiness or retry
//   - failure: an ordinary error at the observed boundary
//
// Progress quantities and resource custody are separate from these controls.
// Dispatch uses [code.hybscloud.com/iox.Classify]: when an error matches both
// semantic sentinels, WouldBlock takes precedence and the continuation stays owned.
// [Dispatcher] requires a valid reply on nil or ErrMore and retains partial
// work on WouldBlock. Whole-program completion is indicated by the absence of
// a successor suspension, not merely by a successful dispatch.
//
// Event-loop backends report infrastructure poll failures directly from
// [Backend.Poll]. [Loop.Poll] and [Loop.Run] keep poll failures separate from
// completion classification: poll-level [code.hybscloud.com/iox.ErrWouldBlock]
// is treated as idle, while completion-level
// [code.hybscloud.com/iox.ErrWouldBlock] defers the same suspension to a later
// offer round without marking that tick as progress. This requires retirement
// with no reply or hidden partial progress. [Backend.Submit] must
// return a token that is unique among all submissions still live in the loop;
// if a backend reuses a live token, [Loop.Poll] and [Loop.Run] surface
// [ErrLiveTokenReuse]. Completion-level [code.hybscloud.com/iox.ErrMore] records
// [ErrUnsupportedMultishot]: its submission remains live and cannot resume the
// same affine continuation repeatedly. Use [SubscriptionLoop] with a binding
// that owns the native stream lifetime.
//
// The full fetched batch is accounted before replies resume. A batch fault
// may still permit earlier stable replies to resume; their successors and the
// remaining suspensions are discarded under the binding's abort contract.
// [Loop.Poll] returns completed prefix results first and reports the stored
// error on a later call. [Loop.Run] returns collected results with that error.
//
// [SubscriptionLoop] is the abstract stream counterpart to [Loop]. It uses a
// [RouteID] made from a [Token] and generation, returns opaque [Subscription]
// handles, and polls [StreamCompletion] values. [NewSubscriptionLoop] requires
// a positive route capacity. When all slots are in use, [SubscriptionLoop.Subscribe]
// returns [code.hybscloud.com/iox.ErrWouldBlock] before calling the backend.
// [StreamCompletion.More] is
// route-liveness evidence: when it is true, [SubscriptionLoop.Poll] emits a
// non-final [StreamEvent] and keeps the route live; when it is false, Poll emits
// a final event and retires the route. [StreamCompletion.HasValue] and
// [StreamCompletion.EventErr] describe payload evidence at that boundary and
// are independent of More, so an empty live boundary and a payload error on a
// still-live route are both representable without collapsing them into
// terminal success or failure. Poll-level [code.hybscloud.com/iox.ErrWouldBlock]
// leaves route state unchanged. A [SubscriptionBackend] must not return ready stream
// completions together with a non-nil poll-level error; payload errors belong in
// [StreamCompletion.EventErr]. Stale completions for already retired routes are
// ignored.
//
// [CompletionMemory] supplies the [Completion] slice that a [Loop] passes to
// [Backend.Poll]. [NewLoop] requires positive computation capacity, separately
// from completion-batch size. With functional options, [WithMemory] installs
// a custom provider, [WithMaxCompletions] caps the visible slice length,
// [HeapMemory] is the default, and [BoundedMemory] provides a single bounded
// pool of default-sized 128 KiB slabs. [Loop.Drain] releases that slice exactly
// once through [CompletionMemory.Release]. Custom providers must hand each live
// loop an exclusive non-overlapping slab and may treat Release as ownership
// transfer back to the provider.
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
// [AdvanceSuspension] resumes on [code.hybscloud.com/iox.ErrMore] using the
// dispatcher's valid reply while the interaction remains live. It returns the
// original suspension unchanged on [code.hybscloud.com/iox.ErrWouldBlock] or
// ordinary failure.
//
// [Loop] stores pending `*kont.Suspension` frontiers produced by
// [code.hybscloud.com/kont.StepExpr] (or by reifying
// [code.hybscloud.com/kont.Eff] into Expr form first). This gives the backend
// one pending suspension per token while that submission remains live.
//
// [Loop] and [SubscriptionLoop] each have one owner. Serialize all access,
// including Ready, Pending and Failed. Share the constructed loop by pointer
// and call state-changing methods only after the current callback returns.
// The provisioned token index uses fixed bucket chains. Admission reserves before
// program evaluation; backend refusal retains the reached suspension. Poll
// accounts the full batch before resuming. Successors enter later offer rounds.
// [Loop.Ready] distinguishes fresh work from deferred retries. Native custody
// remains with the backend binding even after local disposal.
//
// [Loop.Poll] results are borrowed until the next Poll, Run or Drain;
// [SubscriptionLoop.Poll] events until the next Poll or Drain. Copy any values
// that must survive that boundary. The runners clear expired references; this
// does not dispose native resources. [Loop.Run]'s collector is caller-owned.
//
// # Execution styles
//
// Blocking execution, manual stepping and backend-driven execution have different
// scheduling contracts. Equivalence requires matching operation, ownership,
// scheduling and failure premises; determinism alone does not supply them.
package takt
