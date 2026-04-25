[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**English** | [简体中文](README.zh-CN.md) | [Español](README.es.md) | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

An abstract completion-driven dispatch engine for non-blocking I/O stacks.

## Overview

In a proactor model, I/O operations are submitted to the kernel and their completions arrive asynchronously. The application must correlate each completion back to the computation that requested it, resume that computation, and handle the full range of outcomes — success, partial progress, backpressure, and failure.

`takt` provides this execution model as an abstract layer over the [kont](https://code.hybscloud.com/kont) effect
system. A `Dispatcher` evaluates one algebraic effect at a time, classifying the result according to
the [iox](https://code.hybscloud.com/iox) outcome algebra. A `Backend` submits operations to an asynchronous engine (for
example, `io_uring`) and polls for completions. The `Loop` ties them together: it submits computations, polls the
backend, correlates completions by token, and resumes suspended continuations.

Two equivalent APIs are available: `kont.Eff` (closure-based, straightforward to compose) and `kont.Expr` (frame-based,
with lower allocation overhead on hot paths).

The event-loop path stores one pending suspension per live token. Each token tracks a suspension produced by
`kont.StepExpr` (or by reifying `kont.Eff` first), so `Backend.Submit` must not reuse a token while the older submission
carrying it remains live in the loop.

## Installation

```bash
go get code.hybscloud.com/takt
```

Requires Go 1.26 or later.

## Outcome Classification

Each dispatched operation yields an `iox` outcome. The dispatcher and stepping API handle each case:

| Outcome | Meaning | Dispatcher | Stepping API |
|---------|---------|------------|-------------|
| `nil` | completed | resume | resume, return `nil` |
| `ErrMore` | progress, more expected | resume | resume, return `ErrMore` |
| `ErrWouldBlock` | no progress | wait | return suspension to caller |
| other | infrastructure failure | panic | return error to caller |

## Usage

### Dispatcher

A `Dispatcher` maps each algebraic effect to a concrete I/O operation and returns the result together with an `iox`
outcome.

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // dispatch op, return (value, nil) or (nil, iox.ErrWouldBlock)
}
```

### Blocking Evaluation

`Exec` and `ExecExpr` run a computation to completion, synchronously waiting when the dispatcher yields `iox.ErrWouldBlock`.

```go
result := takt.Exec(d, computation) // kont.Eff
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### Stepping

For proactor event loops (e.g., `io_uring`), `Step` and `Advance` evaluate one effect at a time. When the dispatcher
yields `iox.ErrWouldBlock`, the suspension is returned to the caller so the event loop can reschedule it.

```go
result, susp := takt.Step[int](exprComputation)
if susp != nil {
    var err error
    result, susp, err = takt.Advance(d, susp)
    if iox.IsWouldBlock(err) {
        return susp // yield to the event loop and reschedule when ready
    }
}
// result holds the final value
```

### Error Handling

Compose dispatcher operations with error effects. `Throw` short-circuits the computation eagerly and discards the
pending suspension.

```go
either := takt.ExecError[string](d, computation)
// Right on success, Left on Throw

// Stepping with errors
either, susp := takt.StepError[string, int](exprComputation)
if susp != nil {
    var err error
    either, susp, err = takt.AdvanceError[string](d, susp)
    if iox.IsWouldBlock(err) {
        return susp // yield to the event loop and reschedule when ready
    }
}
```

### Event Loop

A `Loop` drives computations through a `Backend`. It submits operations, polls for completions, correlates them by
`Token`, and resumes suspended continuations. `NewLoop` accepts functional `Option`s. `WithMaxCompletions(n)` panics
when `n <= 0` with `takt: WithMaxCompletions requires n > 0`; `WithMemory(nil)` panics with
`takt: WithMemory requires a non-nil CompletionMemory`.

`Backend.Poll([]Completion) (int, error)` reports both the number of ready completions and any infrastructure poll
failure. `Loop` treats `iox.ErrWouldBlock` returned by `Poll` as an idle tick rather than a terminal error.

`Loop` is a single-owner runner. Serialize calls that share the same `Loop`, including `SubmitExpr`, `Submit`, `Poll`,
`Run`, `Drain`, `Pending`, and `Failed`.

`Backend.Submit` must return a token that is unique among all submissions still live in the loop. If a backend reuses a
live token, the loop records `ErrLiveTokenReuse`, drains every pending suspension exactly once, and every subsequent
`SubmitExpr` / `Submit` / `Poll` / `Run` call returns that fatal error.

When a completion carries `iox.ErrWouldBlock`, the loop resubmits the same operation. If a completion carries
`iox.ErrMore` (multishot), the loop records `ErrUnsupportedMultishot`, drains every pending suspension exactly once, and
every subsequent `SubmitExpr` / `Submit` / `Poll` / `Run` call returns that fatal error. `ErrMore` means the submitted
backend operation remains active after the CQE, while generic `Loop` has no subscription/cancel carrier for later
same-token completions.

`Loop.Failed()` reports the recorded fatal error (or `nil`). `Loop.Drain()` forces the loop into a disposed state,
discards every pending suspension exactly once, and records `ErrDisposed` only if no fatal was previously set; it is
idempotent and preserves the original fatal error when one already exists.

```go
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// Submit computations
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // kont.Eff

// Drive all to completion
results, err := loop.Run()
```

`NewLoop` uses [`HeapMemory`](completion_memory.go) as the default completion-buffer provider. Pass
[`BoundedMemory`](completion_memory.go) via [`WithMemory`](option.go) when completion buffers should come from a bounded
steady-state pool of default-sized 128 KiB slabs; or supply a custom `CompletionMemory` implementation to control
allocation strategy without widening the `Backend` or `Completion` contracts. Custom providers must return exclusive
non-overlapping live slabs and may treat `Release` as ownership transfer back to the provider:

```go
// Default: HeapMemory + default-sized completion slab.
loop := takt.NewLoop[*myBackend, int](backend)

// Cap the per-poll completion slab length (the provider still chooses the slab shape; Loop trims the visible length back to this cap).
loop = takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// Share one HeapMemory's sync.Pool across several Loops by passing the same address to WithMemory; copying a HeapMemory value would not share recycled slabs.
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))
loopB := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))

// BoundedMemory: one bounded pool of default-sized 128 KiB slabs. WithPoolCapacity tunes that pool's capacity (rounded up to the next power of two by iobuf).
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
loop = takt.NewLoop[*myBackend, int](
    backend,
    takt.WithMemory(bounded),
    takt.WithMaxCompletions(64),
)
```

## API Overview

### Dispatch

- `Dispatcher[D Dispatcher[D]]` — non-blocking dispatch interface
- `Exec[D, R](d D, m kont.Eff[R]) R` — blocking evaluation of `kont.Eff`
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — blocking evaluation of `kont.Expr`

### Stepping

- `SuspensionLike[S, R]` — resumable interface (`Op` + `Resume`), implemented by `cove.SuspensionView`
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — evaluate until the first suspension
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)` — dispatch one operation through any `SuspensionLike` value
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — dispatch one operation

### Error Handling

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — blocking evaluation with errors
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — error-aware evaluation of `kont.Expr`
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — stepping with errors
-
`AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` —
advance one step with errors

### Backend and Event Loop

- `Backend[B Backend[B]]` — asynchronous submit/poll interface
- `CompletionMemory` — loop-local completion-buffer provider
- `HeapMemory` — default implementation (`sync.Pool`-backed typed slabs of the default size)
- `BoundedMemory` — iobuf-backed implementation with a single bounded pool of default-sized 128 KiB slabs
- `Option` — functional options for `NewLoop` (`WithMemory`, `WithMaxCompletions`)
- `CompletionBufOption` — functional options for `CompletionMemory.CompletionBuf` (`WithSize`)
- `BoundedMemoryOption` — functional options for `NewBoundedMemory` (`WithPoolCapacity`)
- `Token` — submission-completion correlation (`uint64`)
- `Completion` — `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, opts ...Option) *Loop[B, R]` — create an event loop (default `HeapMemory`, default-sized slab)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)` — step and submit Expr
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` — step and submit Cont
- `(*Loop[B, R]).Poll() ([]R, error)` — poll and dispatch completions
- `(*Loop[B, R]).Run() ([]R, error)` — drive all to completion
- `(*Loop[B, R]).Pending() int` — count pending operations
- `(*Loop[B, R]).Failed() error` — terminal fatal error, or nil
- `(*Loop[B, R]).Drain() int` — discard pending suspensions and dispose the loop
- `ErrLiveTokenReuse` — backend reused a token that was still live in the loop
- `ErrUnsupportedMultishot` — multishot completion is unsupported by generic `Loop`
- `ErrDisposed` — loop has been disposed via `Drain`

### Bridge

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Eff

## Practical Recipes

A complete event-loop integration combines a `Dispatcher` (the synchronous semantics) with a `Backend` (the proactor)
under one `Loop`:

```go
// 1. Define the dispatcher: maps an effect operation to an iox outcome.
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // Return (value, nil) on completion or (nil, iox.ErrWouldBlock) to yield.
}

// 2. Define the backend: submits ops to the OS proactor and polls completions.
type myBackend struct{ /* ... */ }
func (b *myBackend) Submit(op kont.Operation) (takt.Token, error) { /* ... */ }
func (b *myBackend) Poll(out []takt.Completion) (int, error)      { /* ... */ }

// 3. Drive: submit one or more computations, then Run to completion.
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))
loop.SubmitExpr(prog1)
loop.SubmitExpr(prog2)
results, err := loop.Run()
_ = results; _ = err
```

For error-aware composition use `ExecError` / `StepError` / `AdvanceError` in place of their non-error counterparts;
`Throw` short-circuits the in-flight computation while leaving sibling computations on the same loop unaffected. The
fused dispatcher+backend pattern shown here is the one used by `sess` to attach a session endpoint to a real I/O
runtime.

## References

- Tarmo Uustalu and Varmo Vene. 2008. Comonadic Notions of Computation. *Electronic Notes in Theoretical Computer
  Science* 203, 5 (June 2008), 263–284. https://doi.org/10.1016/j.entcs.2008.05.029
- Gordon D. Plotkin and Matija Pretnar. 2009. Handlers of Algebraic Effects. In *Proc. 18th European Symposium on
  Programming (ESOP '09)*. LNCS 5502, 80–94. https://doi.org/10.1007/978-3-642-00590-9_7
- Andrej Bauer and Matija Pretnar. 2015. Programming with Algebraic Effects and Handlers. *Journal of Logical and
  Algebraic Methods in Programming* 84, 1 (Jan. 2015), 108–123. https://arxiv.org/abs/1203.1539
- Daniel Leijen. 2017. Type Directed Compilation of Row-Typed Algebraic Effects. In *Proc. 44th ACM SIGPLAN Symposium on
  Principles of Programming Languages (POPL '17)*. 486–499. https://doi.org/10.1145/3009837.3009872
- Danel Ahman and Andrej Bauer. 2020. Runners in Action. In *Proc. 29th European Symposium on Programming (ESOP '20)*.
  LNCS 12075, 29–55. https://arxiv.org/abs/1910.11629
- Daniel Hillerström, Sam Lindley, and Robert Atkey. 2020. Effect Handlers via Generalised Continuations. *Journal of
  Functional Programming* 30 (2020), e5. https://bentnib.org/handlers-cps-journal.pdf

## License

MIT License. See [LICENSE](LICENSE) for details.

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
