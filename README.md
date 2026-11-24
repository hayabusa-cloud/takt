[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Benchmarks](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml/badge.svg)](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**English** | [简体中文](README.zh-CN.md) | [Español](README.es.md) | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

An abstract completion-driven dispatch engine for non-blocking I/O stacks.

## Overview

In the proactor model, a backend accepts I/O requests and reports completions asynchronously. When a completion matches an outstanding request and supplies a valid reply, such as the number of bytes written, the loop resumes the waiting computation.

`takt` provides this execution model as an abstract layer over the [kont](https://code.hybscloud.com/kont) effect system. A `Dispatcher` evaluates one algebraic effect at a time. `Advance` interprets its result according to the [iox](https://code.hybscloud.com/iox) outcome algebra. A `Backend` submits operations to an asynchronous engine, for example `io_uring`, and polls for completions. `Loop` submits operations to the backend, polls it, correlates completions by token, and resumes suspended continuations.

Programs have two representations: `kont.Eff` uses closures for composition; `kont.Expr` exposes frames for stepping. `Reify` and `Reflect` connect them. Allocation and execution costs depend on the program and realization; neither representation alone guarantees a cheaper hot path.

The event-loop path stores one pending suspension per live token. Each token tracks a suspension produced by `kont.StepExpr` (or by reifying `kont.Eff` first), so `Backend.Submit` must not reuse a token while the older submission carrying it remains live in the loop.

For stream or multishot-style integrations, `SubscriptionLoop` provides a separate abstract route runner. It tracks `Subscription` handles by `RouteID` (`Token` plus generation), polls `StreamCompletion` values, emits `StreamEvent` observations, and keeps `More` as route-liveness evidence independent of payload value or payload error. This keeps generic `Loop` affine and one-shot while still giving concrete runtimes a principled place to represent same-operation successor observations.

## Composition Boundary

`takt` owns execution movement, not context meaning or outcome vocabulary. `iox` classifies `nil`, `ErrWouldBlock`, `ErrMore`, and failure; `kont` owns the suspension/resumption carrier; `cove` may wrap a suspension with explicit context through `SuspensionView`; `takt` advances any value that satisfies `SuspensionLike` without interpreting that context.

## Installation

```bash
go get code.hybscloud.com/takt
```

Requires Go 1.27 or later.

## Outcome Classification

Each dispatched operation yields an `iox` outcome. `Dispatcher.Dispatch` reports that outcome as `(value, error)`; blocking runners and the stepping API interpret it as follows:

| Outcome | Meaning | Dispatcher return | Blocking / stepping behavior |
|---------|---------|-------------------|------------------------------|
| `nil` | valid reply | `(value, nil)` | resume |
| `ErrMore` | valid reply; interaction stays live | `(value, ErrMore)` | resume; stepping returns `ErrMore` |
| `ErrWouldBlock` | no reply now; retry later | `(nil, ErrWouldBlock)` | blocking waits; stepping returns the suspension |
| other | infrastructure failure | `(nil, error)` | blocking panics; stepping returns the error |

The table describes the Dispatcher reply contract. `iox` control alone does not measure transferred bytes or establish resource release; partial work must remain with the dispatcher when it returns WouldBlock.

Dispatch follows `iox.Classify`, including wrapped errors. An error matching both `ErrWouldBlock` and `ErrMore` is WouldBlock: retain the suspension and retry later.

## Usage

### Dispatcher

A `Dispatcher` maps each algebraic effect to a concrete I/O operation and returns the result together with an `iox` outcome.

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	// dispatch op; return (value, nil), (value, iox.ErrMore), or (nil, iox.ErrWouldBlock)
}
```

### Blocking Evaluation

`Exec` and `ExecExpr` run a computation to completion, synchronously waiting when the dispatcher yields `iox.ErrWouldBlock`.

```go
result := takt.Exec(d, computation) // kont.Eff
```

```go
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### Stepping

`Step` evaluates an expression until it returns a value or suspends on an operation. `Advance` dispatches that operation directly. If the dispatcher writes some bytes before returning `iox.ErrWouldBlock`, it keeps the byte offset and the caller retains the same suspension for retry.

The manual stepping outcome law is explicit: if `d.Dispatch(susp.Op())` returns `nil` or `iox.ErrMore`, `Advance` resumes the suspension with the returned value and returns the next suspension plus the original error; if it returns `iox.ErrWouldBlock` or an ordinary failure, `Advance` returns the zero result, the original suspension, and that error. `ErrMore` is therefore a progress signal in manual stepping, while completion-level `ErrMore` remains unsupported by generic `Loop` because the backend operation is still live.

```go
result, susp := takt.Step[int](exprComputation)
var err error
if susp != nil {
	result, susp, err = takt.Advance(d, susp)
}
// result is final only when susp == nil. Otherwise retain susp and handle err before the next advance.
```

If permission for a pending operation can be revoked, call `cove.CheckSuspension` with current context. If the check fails, keep the original suspension. If it succeeds, pass the view to `AdvanceSuspension` and keep permission valid through the call. After `iox.ErrWouldBlock`, recheck on the next relevant event. [Guarded write example](write_session_example_test.go)

### Error Handling

On a matching `Throw`, `AdvanceError` discards the pending suspension and returns `Left(e)`.

```go
either := takt.ExecError[string](d, computation)
// Right on success, Left on Throw

either, susp := takt.StepError[string, int](exprComputation)
var err error
if susp != nil {
	either, susp, err = takt.AdvanceError[string](d, susp)
}
// either is final only when susp == nil. Retain the suspension on WouldBlock or infrastructure failure.
```

For a checked, pending `cove.SuspensionView[C, kont.Either[E, A]]`, use `either, view.Suspension, err = takt.AdvanceError(d, view.Suspension)`. When `view.Suspension == nil`, `either` is final and `view.Ctx` is still available.

### Event Loop

A `Loop` drives computations through a `Backend`. It submits operations, polls for completions, correlates them by `Token`, and resumes suspended continuations. `NewLoop` accepts functional `Option`s. `WithMaxCompletions(n)` panics when `n <= 0` with `takt: WithMaxCompletions requires n > 0`; `WithMemory(nil)` panics with `takt: WithMemory requires a non-nil CompletionMemory`.

`NewLoop` and `NewSubscriptionLoop` require positive capacity. `Loop.Submit` and `SubmitExpr` reserve before evaluating; their success admits work locally, and `Poll` offers it to the backend. A pure program can finish immediately: check the returned `done` flag and value. Local capacity refusal executes no prefix; `SubmitSuspension` transfers ownership only on success. `Ready` distinguishes fresh work from deferred retries. `WithMaxCompletions` bounds offers, observations, and resumptions in separate phases. See the [execution contract](docs/execution.md) and [proof scope](proofs/README.md).

`Backend.Poll([]Completion) (int, error)` reports both the number of ready completions and any infrastructure poll failure; a backend must not return ready completions together with a non-nil poll-level error. `Loop` treats `iox.ErrWouldBlock` returned by `Poll` as an idle tick rather than a terminal error.

On a successful or ordinary-failure completion, `Loop` resumes with `Completion.ResumeValue()`, including any operation error in that reply. Ordinary backends populate `Value`; fixed non-pointer payloads can use `NewInlineCompletion` and a `CompletionDecoder` to delay boxing until resumption. Reading `Value` directly does not decode inline payloads; decoding may allocate.

`Loop` and `SubscriptionLoop` require serialized access, including observation methods such as `Ready`, `Pending`, and `Failed`. Callbacks must not reenter state-changing methods. Do not copy a constructed loop.

`Backend.Submit` must return a token that is unique among all submissions still live in the loop. Tokens are correlation keys, not sequence numbers; a concrete backend may use a kernel `user_data` value directly. If a backend reuses a live token, the loop records `ErrLiveTokenReuse`, drains every pending suspension exactly once, and every subsequent `SubmitExpr` / `Submit` / `Poll` / `Run` call returns that fatal error.

A completion carrying `iox.ErrWouldBlock` defers the same suspension to a later round, provided the old submission retired with no reply or hidden partial progress. Completion-level `iox.ErrMore` records `ErrUnsupportedMultishot`: that backend interaction remains live and needs a subscription owner. The loop accounts the whole batch before resuming. Earlier stable replies may still complete; successors and remaining suspensions are discarded under the binding's abort contract. `Poll` returns completed prefix results first and reports the stored error on a later call; `Run` returns collected results together with the error.

`Loop.Poll` results are borrowed until the next `Poll`, `Run`, or `Drain`; stream events until the next `Poll` or `Drain`. Copy values before retaining them. Expiration clears the runner's references, including obsolete event-buffer tails. Clearing is not resource disposal: native requests, descriptors, and buffer loans retain their own owners. Call `Loop.Drain` even after completion or failure to return its completion slab.

`Loop.Failed()` reports the recorded fatal error (or `nil`). `Loop.Drain()` forces the loop into a disposed state, discards every pending suspension exactly once, and records `ErrDisposed` only if no fatal was previously set; it is idempotent and preserves the original fatal error when one already exists.

```go
loop := takt.NewLoop[*myBackend, int](backend, 128, takt.WithMaxCompletions(64))
defer loop.Drain()

result, done, err := loop.SubmitExpr(exprComputation)
if err != nil {
	return err
}
if done {
	fmt.Println(result)
} else {
	results, err := loop.Run()
	fmt.Println(results)
	if err != nil {
		return err
	}
}
```

### Stream / Subscription Runner

`SubscriptionLoop` is the sibling runner for route-indexed stream observations. A `SubscriptionBackend` starts an operation with `Subscribe`, polls `StreamCompletion` values, and accepts `Cancel` requests for live routes. `Subscribe` returns `iox.ErrWouldBlock` before calling the backend when all route slots are in use. A `Subscription` belongs to the loop that issued it. When reusing a `Token`, the backend must use a new generation in the `RouteID` while an old handle or completion can still reach the loop.

`StreamCompletion.More` means the same route remains live after the observation. `HasValue` and `EventErr` describe payload evidence at that boundary and are independent of `More`: a completion can report a value and more to come, no value and more to come, or a payload error while the route remains live. A completion with `More == false` emits a final `StreamEvent` and retires the route.

`Subscribe` rejects the zero `RouteID` with `ErrInvalidRouteID` and live-route aliasing with `ErrLiveRouteReuse`; both conditions put the stream loop in a fatal state. Poll-level `iox.ErrWouldBlock` is an idle tick. A `SubscriptionBackend` must not return ready stream completions together with a non-nil poll-level error; payload errors belong in `StreamCompletion.EventErr`. Unknown or already retired route completions are stale observations and are ignored. `Cancel` requests route cancellation without retiring the route immediately; a terminal completion, `Drain`, or a fatal loop transition retires it.

```go
type myStreamBackend struct{ /* ... */ }

func (b *myStreamBackend) Subscribe(op kont.Operation) (takt.RouteID, error) {
	return takt.NewRouteID(tok, generation), nil
}

func (b *myStreamBackend) Poll(out []takt.StreamCompletion[int]) (int, error) {
	// Fill out with route-indexed observations.
	return n, nil
}

func (b *myStreamBackend) Cancel(id takt.RouteID) error {
	// Request cancellation of the live route.
	return nil
}

stream := takt.NewSubscriptionLoop[*myStreamBackend, int](
	backend, 128,
	takt.WithMaxStreamCompletions(64),
)
defer stream.Drain()

sub, err := stream.Subscribe(op)
if err != nil {
	return err
}
events, err := stream.Poll()
if err != nil {
	return err
}
fmt.Println(sub.ID(), events)
```

### Completion Memory

`NewLoop` uses [`HeapMemory`](completion_memory.go) as the default completion-buffer provider. Pass [`BoundedMemory`](completion_memory.go) via [`WithMemory`](option.go) when completion buffers should come from a bounded steady-state pool of default-sized 128 KiB slabs; or supply a custom `CompletionMemory` implementation to control allocation strategy without widening the `Backend` or `Completion` contracts. Custom providers must return exclusive non-overlapping live slabs and may treat `Release` as ownership transfer back to the provider:

```go
// Default: HeapMemory + default-sized completion slab.
defaultLoop := takt.NewLoop[*myBackend, int](backend, 128)
defer defaultLoop.Drain()

// Cap the per-poll completion slab length (the provider still chooses the slab shape; Loop trims the visible length back to this cap).
cappedLoop := takt.NewLoop[*myBackend, int](backend, 128, takt.WithMaxCompletions(64))
defer cappedLoop.Drain()

// Share the same HeapMemory pointer between loops. Do not copy HeapMemory or BoundedMemory after first use.
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backendA, 128, takt.WithMemory(heap))
defer loopA.Drain()
loopB := takt.NewLoop[*myBackend, int](backendB, 128, takt.WithMemory(heap))
defer loopB.Drain()

// BoundedMemory: one bounded pool of default-sized 128 KiB slabs. WithPoolCapacity tunes that pool's capacity (rounded up to the next power of two by iobuf).
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
boundedLoop := takt.NewLoop[*myBackend, int](
	backend, 128,
	takt.WithMemory(bounded),
	takt.WithMaxCompletions(64),
)
defer boundedLoop.Drain()
```

`BoundedMemory` bounds retained pooled storage, not total live memory. An exhausted pool allocates an extra default-sized slab; oversized requests allocate exact-size slabs. `Release` does not retain those overflow slabs. Call `Drain` on every loop to return its storage, even after completion or failure; native resource settlement remains the binding's responsibility.

## API Overview

### Dispatch

- `Dispatcher[D Dispatcher[D]]`: non-blocking dispatch interface
- `Exec[D, R](d D, m kont.Eff[R]) R`: blocking evaluation of `kont.Eff`
- `ExecExpr[D, R](d D, m kont.Expr[R]) R`: blocking evaluation of `kont.Expr`

### Stepping

- `SuspensionLike[S, R]`: resumable interface (`Op` + `Resume`), implemented by `cove.SuspensionView`
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])`: evaluate until the first suspension
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)`: dispatch one operation through any `SuspensionLike` value
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)`: dispatch one operation

### Error Handling

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]`: blocking evaluation with errors
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]`: error-aware evaluation of `kont.Expr`
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])`: stepping with errors
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)`: advance one step with errors

### Backend and Event Loop

- `Backend[B Backend[B]]`: asynchronous submit/poll interface
- `CompletionMemory`: loop-local completion-buffer provider
- `HeapMemory`: default implementation (`sync.Pool`-backed typed slabs of the default size)
- `BoundedMemory`: iobuf-backed implementation with a single bounded pool of default-sized 128 KiB slabs
- `Option`: functional options for `NewLoop` (`WithMemory`, `WithMaxCompletions`)
- `CompletionBufOption`: functional options for `CompletionMemory.CompletionBuf` (`WithSize`)
- `BoundedMemoryOption`: functional options for `NewBoundedMemory` (`WithPoolCapacity`)
- `Token`: submission-completion correlation (`uint64`)
- `Completion`: token, outcome and direct or inline reply payload
- `Completion.ResumeValue() kont.Resumed`: obtain the reply from either representation
- `CompletionDecoder`: decode two non-pointer words at resumption
- `NewInlineCompletion(token Token, word0, word1 uint64, decoder CompletionDecoder, err error) Completion`: construct an inline completion
- `NewLoop[B, R](b B, capacity int, opts ...Option) *Loop[B, R]`: create an event loop (default `HeapMemory`, default-sized slab)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)`: reserved evaluation; immediate result or queued suspension
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)`: reserved reification; immediate result or queued suspension
- `(*Loop[B, R]).Poll() ([]R, error)`: poll and dispatch completions
- `(*Loop[B, R]).Run() ([]R, error)`: drive all to completion
- `(*Loop[B, R]).SubmitSuspension(s *kont.Suspension[R]) error`
- `(*Loop[B, R]).Ready() bool`
- `(*Loop[B, R]).Pending() int`: local computation lineages, including deferred retries
- `(*Loop[B, R]).Failed() error`: terminal fatal error, or nil
- `(*Loop[B, R]).Drain() int`: discard pending suspensions and dispose the loop
- `ErrLiveTokenReuse`: backend reused a token that was still live in the loop
- `ErrUnsupportedMultishot`: multishot completion is unsupported by generic `Loop`
- `ErrDisposed`: loop has been disposed via `Drain`

### Stream Routes

- `RouteID`: stream route identity (`Token` plus generation)
- `NewRouteID(token Token, generation uint64) RouteID`: construct a route identifier for stream backends
- `RouteID.Token() Token`: token component
- `RouteID.Generation() uint64`: generation component
- `RouteID.IsZero() bool`: reserved invalid-route test
- `Subscription[A]`: opaque stream route identity
- `Subscription[A].ID() RouteID`: route identity carried by the handle
- `Subscription[A].IsZero() bool`: zero-handle test; does not query route liveness
- `StreamCompletion[A]`: `{ID, Value, HasValue, EventErr, More}`
- `StreamCompletion[A].RouteOutcome() iox.Outcome`: projection to `iox` outcome vocabulary
- `StreamEvent[A]`: `{Subscription, Value, HasValue, Final, EventErr}`
- `SubscriptionBackend[B, A]`: abstract stream backend interface (`Subscribe`, `Poll`, `Cancel`)
- `SubscriptionOption`: functional options for `NewSubscriptionLoop`
- `WithMaxStreamCompletions(n)`: cap the per-poll stream completion buffer
- `NewSubscriptionLoop[B, A](b B, capacity int, opts ...SubscriptionOption) *SubscriptionLoop[B, A]`: create a stream runner
- `(*SubscriptionLoop[B, A]).Subscribe(op kont.Operation) (Subscription[A], error)`: start a route-producing operation
- `(*SubscriptionLoop[B, A]).Poll() ([]StreamEvent[A], error)`: poll route-indexed events
- `(*SubscriptionLoop[B, A]).Cancel(sub Subscription[A]) error`: request route cancellation
- `(*SubscriptionLoop[B, A]).Pending() int`: count live stream routes
- `(*SubscriptionLoop[B, A]).Failed() error`: terminal fatal error, or nil
- `(*SubscriptionLoop[B, A]).Drain() int`: cancel or retire owned routes and dispose the stream loop
- `ErrLiveRouteReuse`: backend reused a route that was still live
- `ErrInvalidRouteID`: backend returned the reserved zero `RouteID`
- `ErrUnknownSubscription`: subscription handle is not live in the stream loop

### Bridge

- `Reify[A](kont.Eff[A]) kont.Expr[A]`: Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]`: Expr → Eff

## Practical Recipes

Use a `Backend` with `Loop`, or a `Dispatcher` with `Exec` and `Advance`. They are separate execution paths; a Loop does not invoke a Dispatcher automatically. `ExecError` and `AdvanceError` interpret application error effects explicitly. A raw Loop needs a backend/interpreter that handles every admitted operation, including any error effects. [Runnable examples](example_test.go) demonstrate successor handling, immediate results, and deferred backend admission.

[Verification](docs/verification.md) includes the installed-tool check command and proof scope. The [performance guide](docs/performance.md) covers locality, scheduling, backoff, typed dispatch and repeatable benchmarks.

## References

- Tarmo Uustalu and Varmo Vene. 2008. Comonadic Notions of Computation. *Electronic Notes in Theoretical Computer Science* 203, 5 (June 2008), 263–284. https://doi.org/10.1016/j.entcs.2008.05.029
- Gordon D. Plotkin and Matija Pretnar. 2009. Handlers of Algebraic Effects. In *Proc. 18th European Symposium on Programming (ESOP '09)*. LNCS 5502, 80–94. https://doi.org/10.1007/978-3-642-00590-9_7
- Andrej Bauer and Matija Pretnar. 2015. Programming with Algebraic Effects and Handlers. *Journal of Logical and Algebraic Methods in Programming* 84, 1 (Jan. 2015), 108–123. https://arxiv.org/abs/1203.1539
- Daniel Leijen. 2017. Type Directed Compilation of Row-Typed Algebraic Effects. In *Proc. 44th ACM SIGPLAN Symposium on Principles of Programming Languages (POPL '17)*. 486–499. https://doi.org/10.1145/3009837.3009872
- Danel Ahman and Andrej Bauer. 2020. Runners in Action. In *Proc. 29th European Symposium on Programming (ESOP '20)*. LNCS 12075, 29–55. https://arxiv.org/abs/1910.11629
- Daniel Hillerström, Sam Lindley, and Robert Atkey. 2020. Effect Handlers via Generalised Continuations. *Journal of Functional Programming* 30 (2020), e5. https://bentnib.org/handlers-cps-journal.pdf

## License

MIT License. See [LICENSE](LICENSE) for details.

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
