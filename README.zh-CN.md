[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | **简体中文** | [Español](README.es.md) | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

面向非阻塞 I/O 栈的抽象完成事件驱动调度引擎。

## 概述

在 proactor 模型中，I/O 操作会提交给内核，其完成事件随后异步到达。应用程序必须把每个完成事件重新关联到发起它的计算，恢复该计算，并处理完整的结果范围——成功、部分进展、背压和失败。

`takt` 将这一执行模型作为 [kont](https://code.hybscloud.com/kont) 效果系统之上的抽象层提供。`Dispatcher`
一次求值一个代数效果，并依据 [iox](https://code.hybscloud.com/iox) 的结果代数对结果进行分类。`Backend` 将操作提交到异步引擎（例如
`io_uring`）并轮询完成事件。`Loop` 将这些部分串联起来：提交计算、轮询后端、通过令牌关联完成事件，并恢复挂起的续延。

提供两套等价 API：`kont.Eff` 基于闭包，组合直接；`kont.Expr` 基于帧，在热路径上具有更低的分配开销。

事件循环路径为每个存活 token 只保存一个挂起。每个 token 跟踪由 `kont.StepExpr` （或先将 `kont.Eff` reify 之后）产生的挂起，因此
`Backend.Submit` 不能在旧提交仍然存活于循环中时复用该 token。

## 安装

```bash
go get code.hybscloud.com/takt
```

需要 Go 1.26 及以上版本。

## 结果分类

每个被调度的操作都会返回一个 `iox` 结果。调度器和步进 API 对各类情况的处理如下：

| 结果              | 含义         | 调度器   | 步进 API           |
|-----------------|------------|-------|------------------|
| `nil`           | 已完成        | 恢复    | 恢复，并返回 `nil`     |
| `ErrMore`       | 有进展，预计还有后续 | 恢复    | 恢复，并返回 `ErrMore` |
| `ErrWouldBlock` | 无进展        | 等待    | 将挂起返回给调用方        |
| 其他              | 基础设施故障     | panic | 将错误返回给调用方        |

## 用法

### 调度器

`Dispatcher` 将每个代数效果映射为具体的 I/O 操作，并返回结果及其对应的 `iox` 状态。

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
// 派发 op，并返回 (value, nil) 或 (nil, iox.ErrWouldBlock)
}
```

### 阻塞求值

`Exec` 和 `ExecExpr` 会一直运行计算直到完成；当调度器返回 `iox.ErrWouldBlock` 时，它们会同步等待。

```go
result := takt.Exec(d, computation) // kont.Eff
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### 步进

对于 proactor 事件循环（例如 `io_uring`），`Step` 和 `Advance` 每次只求值一个效果。当调度器返回 `iox.ErrWouldBlock`
时，挂起会返回给调用方，以便事件循环稍后重新调度。

```go
result, susp := takt.Step[int](exprComputation)
if susp != nil {
    var err error
    result, susp, err = takt.Advance(d, susp)
    if iox.IsWouldBlock(err) {
        return susp // 将控制权交还给事件循环，待就绪后重新调度
    }
}
// result 即最终值
```

### 错误处理

可以将调度器操作与错误效果组合使用。`Throw` 会立即短路当前计算，并丢弃挂起中的暂停。

```go
either := takt.ExecError[string](d, computation)
// 成功时为 Right，Throw 时为 Left

// 带错误的步进
either, susp := takt.StepError[string, int](exprComputation)
if susp != nil {
    var err error
    either, susp, err = takt.AdvanceError[string](d, susp)
    if iox.IsWouldBlock(err) {
        return susp // 将控制权交还给事件循环，待就绪后重新调度
    }
}
```

### 事件循环

`Loop` 通过 `Backend` 驱动计算。它提交操作、轮询完成事件、通过 `Token` 进行关联，并恢复挂起的续延。`NewLoop` 接受函数式
`Option`。`WithMaxCompletions(n)` 在 `n <= 0` 时会以 `takt: WithMaxCompletions requires n > 0` panic；`WithMemory(nil)` 会以
`takt: WithMemory requires a non-nil CompletionMemory` panic。

`Backend.Poll([]Completion) (int, error)` 同时报告就绪完成事件的数量以及基础设施层面的轮询失败。`Loop` 会把 `Poll` 返回的
`iox.ErrWouldBlock` 视为空闲轮询，而不是终止性错误。

`Backend.Submit` 必须在循环中所有仍然存活的提交之间返回唯一 token。如果后端复用了一个存活 token，循环会记录
`ErrLiveTokenReuse`，将所有挂起恰好丢弃一次，并使后续所有 `SubmitExpr` / `Submit` / `Poll` / `Run` 调用都返回该致命错误。

当完成事件携带 `iox.ErrWouldBlock` 时，循环会重新提交同一操作。如果 `iox.ErrMore`（多次触发）完成事件将恢复到一个新的挂起效果，循环会记录
`ErrUnsupportedMultishot`，将所有挂起恰好丢弃一次，并使后续所有 `SubmitExpr` / `Submit` / `Poll` / `Run` 调用都返回该致命错误。
这一拒绝维持了循环的 token 到挂起的关联：多次触发的谱系可以继续恢复当前挂起，但不能在旧提交之下创建新的待处理效果。

`Loop.Failed()` 报告已记录的致命错误（或 `nil`）。`Loop.Drain()` 会强制循环进入已废弃状态，恰好一次地丢弃所有挂起，仅在尚未设置致命错误时记录
`ErrDisposed`；该方法幂等，并保留任何既有的致命错误。

```go
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// 提交计算
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // kont.Eff

// 驱动至全部完成
results, err := loop.Run()
```

`NewLoop` 默认使用 [`HeapMemory`](completion_memory.go) 作为完成事件缓冲区的提供器。如果希望这些缓冲区来自一个保存默认尺寸
128 KiB slab 的有界稳态池，可以通过 [`WithMemory`](option.go) 传入 [`BoundedMemory`](completion_memory.go)；也可以实现自定义
`CompletionMemory`，在不扩展 `Backend` 与 `Completion` 契约的前提下控制分配策略。自定义提供器必须返回彼此独占且不重叠的存活
slab，并且可以把 `Release` 视为所有权回交给提供器：

```go
// 默认：HeapMemory + 默认尺寸完成事件缓冲区。
loop := takt.NewLoop[*myBackend, int](backend)

// 限制单次轮询可见的完成事件缓冲区长度（底层缓冲区形状仍由提供器决定，Loop 只把可见长度裁剪到该上限）。
loop = takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// 通过把同一指针传给 WithMemory，让多个 Loop 共享同一个 HeapMemory 的 sync.Pool；按值复制 HeapMemory 不会共享回收后的缓冲区。
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))
loopB := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))

// BoundedMemory：一个有界池，保存默认尺寸 128 KiB 的 slab。WithPoolCapacity 用于调整该池容量（iobuf 会向上取整到最近的 2 的幂）。
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
loop = takt.NewLoop[*myBackend, int](
    backend,
    takt.WithMemory(bounded),
    takt.WithMaxCompletions(64),
)
```

## API 概览

### 调度

- `Dispatcher[D Dispatcher[D]]` — 非阻塞调度接口
- `Exec[D, R](d D, m kont.Eff[R]) R` — 对 `kont.Eff` 的阻塞求值
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — 对 `kont.Expr` 的阻塞求值

### 步进

- `SuspensionLike[S, R]` — 暴露 `Op` 与 `Resume` 的可恢复接口；`cove.SuspensionView` 实现了该接口
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — 求值直到首次挂起
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)` — 对任意 `SuspensionLike` 值派发一次操作
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — 派发一次操作

### 错误处理

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — 带错误的阻塞求值
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — 对 `kont.Expr` 的带错误求值
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — 带错误的步进
-
`AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` —
带错误地推进一步

### 后端与事件循环

- `Backend[B Backend[B]]` — 异步 submit/poll 接口
- `CompletionMemory` — 循环本地的完成事件缓冲区提供器
- `HeapMemory` — 默认实现（基于 `sync.Pool` 的默认尺寸类型化 slab）
- `BoundedMemory` — 基于 iobuf 的实现，带一个保存默认尺寸 128 KiB slab 的有界池
- `Option` — `NewLoop` 的函数式选项（`WithMemory`, `WithMaxCompletions`）
- `CompletionBufOption` — `CompletionMemory.CompletionBuf` 的函数式选项（`WithSize`）
- `BoundedMemoryOption` — `NewBoundedMemory` 的函数式选项（`WithPoolCapacity`）
- `Token` — 提交与完成的关联标识（`uint64`）
- `Completion` — `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, opts ...Option) *Loop[B, R]` — 创建事件循环（默认使用 `HeapMemory` 与默认尺寸 slab）
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)` — 推进一步并提交 `kont.Expr`
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` — 推进一步并提交 `kont.Eff`
- `(*Loop[B, R]).Poll() ([]R, error)` — 轮询并派发完成事件
- `(*Loop[B, R]).Run() ([]R, error)` — 驱动至全部完成
- `(*Loop[B, R]).Pending() int` — 待处理操作数
- `(*Loop[B, R]).Failed() error` — 终端致命错误；未发生时为 nil
- `(*Loop[B, R]).Drain() int` — 丢弃挂起并废弃循环
- `ErrLiveTokenReuse` — 后端复用了一个在循环中仍然存活的 token
- `ErrUnsupportedMultishot` — 多次触发完成不能挂起到新的效果
- `ErrDisposed` — 循环已通过 `Drain` 废弃

### 桥接

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Eff

## 实用范式

完整的事件循环集成会把 `Dispatcher`（同步语义）与 `Backend`（proactor）统一放在一个 `Loop` 之下：

```go
// 1. 定义 dispatcher：将一个效果操作映射到 iox 的结果。
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // 完成时返回 (value, nil)，需要让出时返回 (nil, iox.ErrWouldBlock)。
}

// 2. 定义 backend：将操作提交到 OS proactor，并轮询完成事件。
type myBackend struct{ /* ... */ }
func (b *myBackend) Submit(op kont.Operation) (takt.Token, error) { /* ... */ }
func (b *myBackend) Poll(out []takt.Completion) (int, error)      { /* ... */ }

// 3. 驱动：提交一个或多个计算，然后 Run 直至全部完成。
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))
loop.SubmitExpr(prog1)
loop.SubmitExpr(prog2)
results, err := loop.Run()
_ = results; _ = err
```

如需带错误处理的组合，可使用 `ExecError` / `StepError` / `AdvanceError` 替代各自的无错版本；`Throw`
会短路当前进行中的计算，同时不会影响同一循环上的其他兄弟计算。这里展示的 dispatcher+backend 融合范式，正是 `sess`
用来把会话端点接到真实 I/O 运行时的方式。

## 参考文献

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

## 许可证

MIT 许可证。详见 [LICENSE](LICENSE)。

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
