[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | **简体中文** | [Español](README.es.md) | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

面向非阻塞 I/O 栈的抽象完成事件驱动调度引擎。

## 概述

在 proactor 模型中，I/O 操作被提交到内核，其完成事件异步到达。应用程序必须将每个完成事件关联回请求它的计算，恢复该计算，并处理全部结果范围 — 成功、部分进展、背压和失败。

`takt` 将此调度代数作为 [kont](https://code.hybscloud.com/kont) 效果系统之上的抽象层提供。`Dispatcher` 一次求值一个代数效果，根据 [iox](https://code.hybscloud.com/iox) 结果代数对结果进行分类。`Backend` 将操作提交到异步引擎（如 `io_uring`）并轮询完成事件。`Loop` 事件循环将它们联系在一起：提交计算、轮询后端、通过令牌关联完成事件并恢复挂起的续延。

两种等价的 API：Cont（基于闭包，直接组合）和 Expr（去函数化，热路径的较低分配开销）。

## 安装

```bash
go get code.hybscloud.com/takt
```

Requires Go 1.26+.

## 结果分类

每个调度的操作返回一个 `iox` 结果。调度器和步进 API 处理每种情况：

| 结果 | 含义 | 调度器 | 步进 API |
------|------|--------|----------|
 `nil` | 已完成 | 恢复 | 恢复，返回 `nil` |
 `ErrMore` | 有进展，预期更多 | 恢复 | 恢复，返回 `ErrMore` |
 `ErrWouldBlock` | 无进展 | 等待 | 将暂停返回给调用者 |
 其他 | 基础设施故障 | panic | 将错误返回给调用者 |

## 用法

### 调度器

`Dispatcher` 将每个代数效果映射到具体的 I/O 操作，并返回带有 `iox` 结果的值。

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // dispatch op, return (value, nil) or (nil, iox.ErrWouldBlock)
}
```

### 阻塞求值

`Exec` 和 `ExecExpr` 运行计算直到完成，当调度器返回 `iox.ErrWouldBlock` 时同步等待。

```go
result := takt.Exec(d, computation)         // Cont-world
result := takt.ExecExpr(d, exprComputation) // Expr-world
```

### 步进

对于 proactor 事件循环（如 `io_uring`），`Step` 和 `Advance` 一次求值一个效果。当调度器返回 `iox.ErrWouldBlock` 时，暂停被返回给调用者，让事件循环重新调度。

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

### 错误处理

将调度器效果与错误效果组合。`Throw` 立即短路计算并丢弃挂起的暂停。

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

### 事件循环

`Loop` 通过 `Backend` 驱动计算。它提交操作、轮询完成事件、通过 `Token` 关联并恢复挂起的续延。
`NewLoop` 的 `maxCompletions` 必须大于 0。

`Backend.Poll([]Completion) (int, error)` 同时报告就绪完成事件数量和基础设施轮询失败。`Loop` 将 `Poll` 返回的
`iox.ErrWouldBlock` 视为空闲轮询，而不是终止性错误。

当完成事件携带 `iox.ErrWouldBlock` 时，循环会在仿射暂停生命周期下重新提交同一操作。如果 `iox.ErrMore`
（多次触发）完成事件将恢复到一个新的挂起效果，`Poll` / `Run` 返回 `ErrUnsupportedMultishot`。

```go
loop := takt.NewLoop[*myBackend, int](backend, 64)

// Submit computations
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // Cont-world

// Drive all to completion
results, err := loop.Run()
```

## API 概览

### 调度

- `Dispatcher[D Dispatcher[D]]` — F-bounded dispatch interface
- `Exec[D, R](d D, m kont.Eff[R]) R` — blocking Cont-world evaluation
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — blocking Expr-world evaluation

### 步进

- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — evaluate to first suspension
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — dispatch one operation

### 错误处理

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — blocking with errors
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — Expr-world with errors
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — step with errors
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` — advance with errors

### 后端与事件循环

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

### 桥接

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Cont

## References

- G. D. Plotkin and M. Pretnar. "Handlers of Algebraic Effects." In *Proc. ESOP*, 2009.
- T. Uustalu and V. Vene. "Comonadic Notions of Computation." In *ENTCS* 203(5), 2008.
- D. Ahman and A. Bauer. "Runners in Action." In *Proc. ESOP*, 2020.

## 许可证

MIT 许可证。详见 [LICENSE](LICENSE)。

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
