# Bounded execution contract

## Dispatch

`Advance` and `AdvanceSuspension` follow `iox.Classify`, including for wrapped
errors. If a dispatch error matches both `iox.ErrWouldBlock` and `iox.ErrMore`,
WouldBlock takes precedence. Stepping returns the original suspension and error.
Blocking runners wait and retry. A dispatch classified as More permits one
resumption with the supplied reply. The dispatcher accounts for any partial progress.

## One-shot computations

`NewLoop(backend, capacity, options...)` provisions a positive number of
computation records. Completion slab size is a separate bound. `Submit` and
`SubmitExpr` reserve before evaluating a program. On local capacity
exhaustion they return `iox.ErrWouldBlock` without executing a prefix.
An immediate return releases the record and reports `done == true`. A pending
operation enters the local queue for `Poll` to offer to the backend.
`SubmitSuspension` transfers an unused non-nil suspension on success. On error
the caller retains it.

Each `Poll` offers at most the completion-slab length of queued operations from
a FIFO snapshot, services the backend, accounts every fetched observation, then
resumes eligible replies in observation order. Each phase has its own count
budget. Successors reuse their existing record and enter a later offer round.
The fixed index admits all valid token distributions. Lookup can cost O(capacity).
Synchronous fatal disposal and `Drain` also cost O(capacity).

Eligible replies retain positions in the current completion slab. There are at
most as many replies as fetched entries, and the resumption budget consumes all
of them before slab clearing or another backend poll. Retaining replies across
rounds would require a different storage contract.

An error from `Backend.Submit` means the backend accepted no work on that
attempt. WouldBlock defers the request. Other submission errors abort the
local runner. The native binding retains accepted requests after a fault.
A completion reporting an operation failure supplies a valid reply and
resumes normally. If `Backend.Poll` returns any completions, its error must be
nil. It can report a later service failure on a subsequent poll.

`Completion.ResumeValue()` returns `Value` for an ordinary completion. For
an inline completion, it invokes the `CompletionDecoder` on the two non-pointer
words supplied to `NewInlineCompletion`. This can avoid boxing during the
backend poll. Decoding may still allocate.

A matched completion classified as More leaves its suspension unresumed and
records `ErrUnsupportedMultishot`. A terminal WouldBlock completion may rearm
only when the old submission has retired with no reply or hidden partial
progress. Both refusal and rearm preserve the suspension as deferred work.
`Pending` counts all locally owned lineages.
`Ready` reports fresh requests or eligible retained replies. External drivers
must combine it with native readiness, deadlines and cleanup obligations before
parking. `Run` retries with `iox.Backoff` and resets it when a suspension resumes.

The [write-session example](../write_session_example_test.go) combines writability
and peer permission with `tempo.Both`. For events on the matching route,
`cove.CheckSuspension` checks both before dispatch. A partial write retains its
suspension and byte offset for the next permitted attempt.

Use `Loop` when the conditions for delivering a reply stay valid through
batch processing. On a batch fault, earlier valid replies can still resume.
The loop discards their successors and the remaining pending suspensions.
If the prefix produced results, `Poll` returns them first and reports the stored
error on the next call. With no results it reports the error immediately.
`Run` returns collected results and the error together.
Resource-producing operations need an independent owner for each undelivered
descriptor, buffer or other value. This abort contract assumes callbacks return
normally. Serialize access to each loop, keep state-changing calls non-reentrant,
and use the constructed loop by pointer.

`Poll` returns a result slice borrowed until the next `Poll`, `Run` or `Drain`.
The runner clears the expired window, including idle and fatal early returns.
Copy the results you need to retain. `Run` collects results in a caller-owned
slice that survives subsequent loop calls. Provisioned runner bookkeeping
allocates no heap storage during transitions. Measure capture construction,
user computation, decoding and backend allocations separately.

## Subscriptions

`NewSubscriptionLoop(backend, capacity, options...)` provisions a fixed number
of routes. At capacity, `Subscribe` returns `iox.ErrWouldBlock` before calling
the backend. Success means the backend accepted the route. Pass the returned
handle only to the loop that issued it. When reusing a token, the backend needs
a fresh generation while an older handle or completion can still reach the loop.
`StreamCompletion.More` keeps the route live regardless of `HasValue` or
`EventErr`. When `More` is false, the event is final and the route retires.
Successful `Cancel` retains the route until terminal observation or local disposal.
`Subscription.IsZero` tests the handle's zero value. A copied nonzero handle
keeps its identity after retirement.
`Drain` retires every local route and requests cancellation once for each route
without an accepted cancellation request. The native binding must retain failed
cancel requests and dispose buffered or late resource-bearing events.
A payload borrowed by the application follows its own release conditions after the route ends.
The event slice is borrowed until the next `Poll` or `Drain`. The loop clears
expired value and error references, including the unused tail after a smaller batch.

## Completion storage and native resources

Completion-slab release requires an explicit `Loop.Drain`, even after a program
finishes or the loop fails. Share `HeapMemory` and `BoundedMemory` providers by
pointer throughout their lifetime.
`BoundedMemory` limits retained pooled storage. Pool exhaustion allocates an extra
default-sized slab. Oversized requests allocate exact-size slabs. Total live
storage can therefore exceed the pool capacity.

`urex.LoopBackend` adapts the native backend for `takt.Loop`. The caller retains
ownership of the backend and its ring. A ring binding retains backing storage,
request identities and cleanup capacity until both target and control requests
can retire, even if `Pending() == 0`.
