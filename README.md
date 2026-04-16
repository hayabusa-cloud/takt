[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**English** | [简体中文](README.zh-CN.md) | [Español](README.es.md) | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

Abstract completion event driven dispatch engine for non-blocking I/O stacks.

## Overview

In a proactor model, I/O operations are submitted to the kernel and their completions arrive asynchronously. The application must correlate each completion back to the computation that requested it, resume that computation, and handle the full range of outcomes — success, partial progress, backpressure, and failure.

`takt` provides this dispatch algebra as an abstract layer over the [kont](https://code.hybscloud.com/kont) effect system. A `Dispatcher` evaluates one algebraic effect at a time, classifying the result according to the [iox](https://code.hybscloud.com/iox) outcome algebra. A `Backend` submits operations to an asynchronous engine (e.g., `io_uring`) and polls for completions. The `Loop` event loop ties them together: it submits computations, polls the backend, correlates completions by token, and resumes the suspended continuations.

Two equivalent APIs: Cont (closure-based, straightforward composition) and Expr (frame-based, lower allocation overhead in hot paths).

## Installation

```bash
go get code.hybscloud.com/takt
```

Requires Go 1.26+.

## Outcome Classification

Every dispatched operation returns an `iox` outcome. The dispatcher and stepping API handle each case:

| Outcome | Meaning | Dispatcher | Stepping API |
|---------|---------|------------|-------------|
| `nil` | completed | resume | resume, return `nil` |
| `ErrMore` | progress, more expected | resume | resume, return `ErrMore` |
| `ErrWouldBlock` | no progress | wait | return suspension to caller |
| other | infrastructure failure | panic | return error to caller |

## Usage

### Dispatcher

A `Dispatcher` maps each algebraic effect to a concrete I/O operation and returns the result with an `iox` outcome.

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // dispatch op, return (value, nil) or (nil, iox.ErrWouldBlock)
}
```

### Blocking Evaluation

`Exec` and `ExecExpr` run a computation to completion, synchronously waiting when the dispatcher yields `iox.ErrWouldBlock`.

```go
result := takt.Exec(d, computation)         // Cont-world
result := takt.ExecExpr(d, exprComputation) // Expr-world
```

### Stepping

For proactor event loops (e.g., `io_uring`), `Step` and `Advance` evaluate one effect at a time. When the dispatcher yields `iox.ErrWouldBlock`, the suspension is returned to the caller, letting the event loop reschedule.

```go
result, susp := takt.Step[int](exprComputation)
if susp != nil {
    var err error
    result, susp, err = takt.Advance(d, susp)
    if iox.IsWouldBlock(err) {
        return susp // yield to event loop, reschedule when ready
    }
}
// result is the final value
```

### Error Handling

Compose dispatcher effects with error effects. `Throw` eagerly short-circuits the computation and discards the pending suspension.

```go
either := takt.ExecError[string](d, computation)
// Right on success, Left on Throw

// Stepping with errors
either, susp := takt.StepError[string, int](exprComputation)
if susp != nil {
    var err error
    either, susp, err = takt.AdvanceError[string](d, susp)
    if iox.IsWouldBlock(err) {
        return susp // yield to event loop, reschedule when ready
    }
}
```

### Event Loop

A `Loop` drives computations through a `Backend`. It submits operations, polls for completions, correlates them by `Token`, and resumes the suspended continuations.
`maxCompletions` in `NewLoop` must be greater than zero.

`Backend.Poll([]Completion) (int, error)` reports both the number of ready completions and any infrastructure poll
failure. `Loop` treats `iox.ErrWouldBlock` from `Poll` as an idle tick rather than a terminal error.

When a completion carries `iox.ErrWouldBlock`, the loop resubmits the same operation under an affine suspension
lifecycle. If an `iox.ErrMore` (multishot) completion would resume into a new suspended effect, `Poll` / `Run` return
`ErrUnsupportedMultishot`.

```go
loop := takt.NewLoop[*myBackend, int](backend, 64)

// Submit computations
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // Cont-world

// Drive all to completion
results, err := loop.Run()
```

## API Overview

### Dispatch

- `Dispatcher[D Dispatcher[D]]` — F-bounded dispatch interface
- `Exec[D, R](d D, m kont.Eff[R]) R` — blocking Cont-world evaluation
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — blocking Expr-world evaluation

### Stepping

- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — evaluate to first suspension
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — dispatch one operation

### Error Handling

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — blocking with errors
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — Expr-world with errors
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — step with errors
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` — advance with errors

### Backend and Event Loop

- `Backend[B Backend[B]]` — F-bounded async submit/poll interface
- `Token` — submission-completion correlation (`uint64`)
- `Completion` — `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, maxCompletions int) *Loop[B, R]` — create event loop (`maxCompletions > 0`)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)` — step and submit Expr
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` — step and submit Cont
- `(*Loop[B, R]).Poll() ([]R, error)` — poll and dispatch completions
- `(*Loop[B, R]).Run() ([]R, error)` — drive all to completion
- `(*Loop[B, R]).Pending() int` — count pending operations
- `ErrUnsupportedMultishot` — multishot completion cannot suspend on a new effect

### Bridge

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Cont

## References

- G. D. Plotkin and M. Pretnar. "Handlers of Algebraic Effects." In *Proc. ESOP*, 2009.
- T. Uustalu and V. Vene. "Comonadic Notions of Computation." In *ENTCS* 203(5), 2008.
- D. Ahman and A. Bauer. "Runners in Action." In *Proc. ESOP*, 2020.

## License

MIT License. See [LICENSE](LICENSE) for details.

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
