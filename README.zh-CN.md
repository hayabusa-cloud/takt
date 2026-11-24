[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Benchmarks](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml/badge.svg)](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | **简体中文** | [Español](README.es.md) | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

面向非阻塞 I/O 栈的抽象完成事件驱动调度引擎。

## 概述

在 Proactor 模型中，后端接受 I/O 请求，并异步报告完成事件。当完成事件与待处理的请求匹配，并带有有效应答（例如已写入的字节数）时，事件循环就恢复等待该应答的计算。

`takt` 将这一执行模型作为 [kont](https://code.hybscloud.com/kont) 效果系统之上的抽象层提供。`Dispatcher` 一次求值一个代数效果。`Advance` 依据 [iox](https://code.hybscloud.com/iox) 的结果代数对结果进行分类。`Backend` 将操作提交到异步引擎，例如 `io_uring`，并轮询完成事件。`Loop` 向后端提交操作、轮询后端、通过令牌关联完成事件，并恢复挂起的续延。

程序有两种表示：`kont.Eff` 使用闭包进行组合，`kont.Expr` 暴露用于逐步执行的帧，二者通过 `Reify` 和 `Reflect` 转换。分配和执行成本取决于程序及其实现；仅选择一种表示并不能保证热路径成本更低。

事件循环为每个活跃的 token 记录一个挂起。`Backend.Submit` 接受操作时，返回的 token 必须在循环中仍然活跃的提交中唯一。

对于流式或 multishot 风格的集成，`SubscriptionLoop` 提供单独的抽象路由运行器。它通过 `RouteID`（`Token` 加 generation）跟踪 `Subscription` 句柄，轮询 `StreamCompletion` 值，发出 `StreamEvent` 观察，并把 `More` 保持为独立于载荷值和载荷错误的路由存活证据。这让通用 `Loop` 保持仿射、一次性，同时为具体运行时表示同一操作的后继观察提供明确位置。

## 组合边界

`takt` 只拥有执行推进，不拥有上下文含义或结果词汇。`iox` 对 `nil`、`ErrWouldBlock`、`ErrMore` 与失败进行分类；`kont` 拥有挂起/恢复载体；`cove` 可以通过 `SuspensionView` 为挂起包裹显式上下文；`takt` 推进任何满足 `SuspensionLike` 的值，但不解释该上下文。

## 安装

```bash
go get code.hybscloud.com/takt
```

需要 Go 1.27 及以上版本。

## 结果分类

每个被调度的操作都会返回一个 `iox` 结果。`Dispatcher.Dispatch` 以 `(value, error)` 报告该结果；阻塞运行器和步进 API 对其解释如下：

| 结果              | 含义         | Dispatcher 返回         | 阻塞/步进行为                   |
|-----------------|------------|-----------------------|---------------------------|
| `nil`           | 有效回复 | `(value, nil)`        | 恢复                        |
| `ErrMore`       | 有效回复；交互仍存活 | `(value, ErrMore)`    | 恢复；步进返回 `ErrMore`          |
| `ErrWouldBlock` | 暂无回复；稍后重试 | `(nil, ErrWouldBlock)` | 阻塞等待；步进返回挂起               |
| 其他              | 基础设施故障     | `(nil, error)`        | 阻塞 panic；步进返回错误            |

此表描述 Dispatcher 的回复契约。`iox` 控制值本身不表示传输字节数，也不证明资源已释放；返回 WouldBlock 时，部分工作必须保留在 dispatcher 中。

分派遵循 `iox.Classify`，包括包装后的错误。同时匹配 `ErrWouldBlock` 和 `ErrMore` 的错误归类为 WouldBlock：保留挂起，稍后重试。

## 用法

### 调度器

`Dispatcher` 将每个代数效果映射为具体的 I/O 操作，并返回结果及其对应的 `iox` 状态。

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	// 派发 op，并返回 (value, nil)、(value, iox.ErrMore) 或 (nil, iox.ErrWouldBlock)
}
```

### 阻塞求值

`Exec` 和 `ExecExpr` 会一直运行计算直到完成；当调度器返回 `iox.ErrWouldBlock` 时，它们会同步等待。

```go
result := takt.Exec(d, computation) // kont.Eff
```

```go
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### 步进

`Step` 对表达式求值，直到它返回一个值或挂起等待某个操作的应答。`Advance` 将这个操作直接交给调度器。如果调度器写入部分数据后返回 `iox.ErrWouldBlock`，它会保存写入偏移量，调用方则保留同一个挂起对象以便重试。

手动步进的结果规则是显式的：如果 `d.Dispatch(susp.Op())` 返回 `nil` 或 `iox.ErrMore`，`Advance` 会用返回值恢复挂起，并返回后继挂起以及原始 error；如果返回 `iox.ErrWouldBlock` 或普通失败，`Advance` 返回零值结果、原始挂起和该 error。`ErrMore` 因此是手动步进中的进展信号；而完成事件层的 `ErrMore` 仍然不被通用 `Loop` 支持，因为后端操作仍然存活。

```go
result, susp := takt.Step[int](exprComputation)
var err error
if susp != nil {
	result, susp, err = takt.Advance(d, susp)
}
// 只有 susp == nil 时 result 才是最终值。否则保留 susp，并在下一次推进前处理 err。
```

如果待执行操作所需的权限可能被撤销，请用当前上下文调用 `cove.CheckSuspension`。检查未通过时，保留原来的挂起对象。检查通过时，将视图传给 `AdvanceSuspension`，并确保权限在整个调用期间有效。遇到 `iox.ErrWouldBlock` 后，在下一个相关事件到来时重新检查权限。[写入前检查权限的示例](write_session_example_test.go)

### 错误处理

如果 `Throw` 的错误类型与预期类型匹配，`AdvanceError` 会丢弃当前的挂起对象并返回 `Left(e)`。

```go
either := takt.ExecError[string](d, computation)
// Right on success, Left on Throw

either, susp := takt.StepError[string, int](exprComputation)
var err error
if susp != nil {
	either, susp, err = takt.AdvanceError[string](d, susp)
}
// 只有 susp == nil 时 either 才是最终值。WouldBlock 或基础设施故障时应保留挂起。
```

如果 `cove.SuspensionView[C, kont.Either[E, A]]` 中的操作仍处于挂起状态且已通过检查，可用 `either, view.Suspension, err = takt.AdvanceError(d, view.Suspension)` 推进。当 `view.Suspension == nil` 时，`either` 是最终结果，`view.Ctx` 仍然可用。

### 事件循环

`Loop` 和 `SubscriptionLoop` 的所有访问都必须串行化，包括 `Ready`、`Pending` 和 `Failed` 等观察方法。回调不得重入会改变状态的方法。构造后的循环不得按值复制。

`NewLoop` 和 `NewSubscriptionLoop` 要求正容量。`Submit` 与 `SubmitExpr` 只进行本地接纳，`Poll` 才向后端提交。容量不足时不会执行程序前缀。`SubmitSuspension` 仅在成功时转移所有权。`Ready` 区分新工作与延迟重试。每轮先处理整个完成批次，再恢复续体；后继操作在后续轮次提交。本地清理不等于原生资源释放。详见[执行契约](docs/execution.md)和[证明范围](proofs/README.md)。

纯计算可能立即完成，因此必须检查 `Submit` 或 `SubmitExpr` 返回的 `done` 和结果值。`WithMaxCompletions` 分别限制提交尝试、观察和恢复阶段的数量。

`Backend.Poll([]Completion) (int, error)` 同时报告就绪完成事件的数量以及基础设施层面的轮询失败；后端不得在返回非 nil 的轮询层 error 时同时返回就绪完成事件。`Loop` 会把 `Poll` 返回的 `iox.ErrWouldBlock` 视为空闲轮询，而不是终止性错误。

完成事件成功或携带普通失败时，`Loop` 使用 `Completion.ResumeValue()` 恢复续体；操作错误也应编码在该回复中。普通后端填充 `Value`；固定的非指针载荷可通过 `NewInlineCompletion` 和 `CompletionDecoder` 将装箱推迟到恢复时。直接读取 `Value` 不会解码内联载荷；解码仍可能分配内存。

`Loop` 是单一所有者运行器。共享同一个 `Loop` 的调用必须串行化，包括 `SubmitExpr`、`Submit`、`Poll`、`Run`、`Drain`、`Pending` 和 `Failed`。

`Backend.Submit` 必须在循环中所有仍然存活的提交之间返回唯一 token。token 是关联键，不是序列号；具体后端可以直接使用 kernel `user_data` 值。如果后端复用了一个存活 token，循环会记录 `ErrLiveTokenReuse`，将所有挂起恰好丢弃一次，并使后续所有 `SubmitExpr` / `Submit` / `Poll` / `Run` 调用都返回该致命错误。

带有 `iox.ErrWouldBlock` 的完成事件会将同一挂起延后到下一轮，前提是旧提交已结束，且没有回复或隐藏的部分进展。完成级 `iox.ErrMore` 会记录 `ErrUnsupportedMultishot`：对应交互仍然存活，需要订阅所有者。循环先处理整个批次，再恢复回复。故障前有效且稳定的回复仍可完成；其后继和其余挂起按绑定的放弃契约处理。`Poll` 先返回已完成的前缀结果，随后调用再报告保存的错误；`Run` 同时返回收集的结果和错误。

`Loop.Poll` 的结果切片借用至下一次 `Poll`、`Run` 或 `Drain`；流事件借用至下一次 `Poll` 或 `Drain`。需要保留时应先复制。借用到期会清除运行器中的引用，包括事件缓冲区未再使用的尾部。清除引用不等于释放资源：原生请求、描述符和缓冲区借用仍由各自所有者负责。即使计算完成或循环失败，也必须调用 `Loop.Drain` 归还完成缓冲区。

`Loop.Failed()` 报告已记录的致命错误（或 `nil`）。`Loop.Drain()` 会强制循环进入已废弃状态，恰好一次地丢弃所有挂起，仅在尚未设置致命错误时记录 `ErrDisposed`；该方法幂等，并保留任何既有的致命错误。

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

### 流/订阅运行器

`SubscriptionLoop` 是面向按路由索引的流观察的同级运行器。`SubscriptionBackend` 使用 `Subscribe` 启动一个操作，轮询 `StreamCompletion` 值，并接收针对存活路由的 `Cancel` 请求。所有路由槽位都已占用时，`Subscribe` 会在调用后端前返回 `iox.ErrWouldBlock`。每个 `Subscription` 只能用于创建它的循环。只要旧句柄或旧完成通知仍有可能到达该循环，后端复用 `Token` 时就必须更新 `RouteID` 的世代号。

`StreamCompletion.More` 表示同一路由在该观察之后仍然存活。`HasValue` 和 `EventErr` 描述该边界上的载荷证据，并且独立于 `More`：一个完成事件可以报告值且还有后续、没有值但还有后续，或者在路由仍然存活时报告载荷错误。`More == false` 的完成事件会发出最终 `StreamEvent` 并退休该路由。

`Subscribe` 会用 `ErrInvalidRouteID` 拒绝零 `RouteID`，并用 `ErrLiveRouteReuse` 拒绝存活路由别名；两者都会使流循环进入致命状态。轮询层面的 `iox.ErrWouldBlock` 是空闲轮询。`SubscriptionBackend` 不得在返回非 nil 的轮询层 error 时同时返回就绪的流完成事件；载荷错误应放在 `StreamCompletion.EventErr`。未知或已经退休的路由完成事件是陈旧观察，会被忽略。`Cancel` 请求取消路由，但不会立即退休该路由；终端完成事件、`Drain` 或致命循环转换会退休它。

```go
type myStreamBackend struct{ /* ... */ }

func (b *myStreamBackend) Subscribe(op kont.Operation) (takt.RouteID, error) {
	return takt.NewRouteID(tok, generation), nil
}

func (b *myStreamBackend) Poll(out []takt.StreamCompletion[int]) (int, error) {
	// 填充按路由索引的观察。
	return n, nil
}

func (b *myStreamBackend) Cancel(id takt.RouteID) error {
	// 请求取消存活路由。
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

### 完成缓冲区内存

`NewLoop` 默认使用 [`HeapMemory`](completion_memory.go) 作为完成事件缓冲区的提供器。如果希望这些缓冲区来自一个保存默认尺寸 512 KiB slab 的有界稳态池，可以通过 [`WithMemory`](option.go) 传入 [`BoundedMemory`](completion_memory.go)；也可以实现自定义 `CompletionMemory`，在不扩展 `Backend` 与 `Completion` 契约的前提下控制分配策略。自定义提供器必须返回彼此独占且不重叠的存活 slab，并且可以把 `Release` 视为所有权回交给提供器：

```go
// 默认：HeapMemory + 默认尺寸完成事件缓冲区。
defaultLoop := takt.NewLoop[*myBackend, int](backend, 128)
defer defaultLoop.Drain()

// 限制单次轮询可见的完成事件缓冲区长度（底层缓冲区形状仍由提供器决定，Loop 只把可见长度裁剪到该上限）。
cappedLoop := takt.NewLoop[*myBackend, int](backend, 128, takt.WithMaxCompletions(64))
defer cappedLoop.Drain()

// 多个循环共享同一 HeapMemory 指针。首次使用后不得复制 HeapMemory 或 BoundedMemory。
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backendA, 128, takt.WithMemory(heap))
defer loopA.Drain()
loopB := takt.NewLoop[*myBackend, int](backendB, 128, takt.WithMemory(heap))
defer loopB.Drain()

// BoundedMemory：一个有界池，保存默认尺寸 512 KiB 的 slab。WithPoolCapacity 用于调整该池容量（iobuf 会向上取整到最近的 2 的幂）。
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
boundedLoop := takt.NewLoop[*myBackend, int](
	backend, 128,
	takt.WithMemory(bounded),
	takt.WithMaxCompletions(64),
)
defer boundedLoop.Drain()
```

`BoundedMemory` 限制池中保留的存储量，而非全部存活内存。池耗尽时会额外分配一个默认尺寸的 slab；超大请求则按所需尺寸分配。`Release` 不会保留这些溢出 slab。每个循环都必须调用 `Drain` 归还存储，即使计算已完成或循环已失败；原生资源的释放仍由绑定负责。

## API 概览

### 调度

- `Dispatcher[D Dispatcher[D]]`: 非阻塞调度接口
- `Exec[D, R](d D, m kont.Eff[R]) R`: 对 `kont.Eff` 的阻塞求值
- `ExecExpr[D, R](d D, m kont.Expr[R]) R`: 对 `kont.Expr` 的阻塞求值

### 步进

- `SuspensionLike[S, R]`: 暴露 `Op` 与 `Resume` 的可恢复接口；`cove.SuspensionView` 实现了该接口
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])`: 求值直到首次挂起
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)`: 对任意 `SuspensionLike` 值派发一次操作
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)`: 派发一次操作

### 错误处理

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]`: 带错误的阻塞求值
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]`: 对 `kont.Expr` 的带错误求值
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])`: 带错误的步进
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)`: 带错误地推进一步

### 后端与事件循环

- `Backend[B Backend[B]]`: 异步 submit/poll 接口
- `CompletionMemory`: 循环本地的完成事件缓冲区提供器
- `HeapMemory`: 默认实现（基于 `sync.Pool` 的默认尺寸类型化 slab）
- `BoundedMemory`: 基于 iobuf 的实现，带一个保存默认尺寸 512 KiB slab 的有界池
- `Option`: `NewLoop` 的函数式选项（`WithMemory`, `WithMaxCompletions`）
- `CompletionBufOption`: `CompletionMemory.CompletionBuf` 的函数式选项（`WithSize`）
- `BoundedMemoryOption`: `NewBoundedMemory` 的函数式选项（`WithPoolCapacity`）
- `Token`: 提交与完成的关联标识（`uint64`）
- `Completion`: token、结果状态以及直接或内联的回复载荷
- `Completion.ResumeValue() kont.Resumed`: 取得任一种表示中的回复
- `CompletionDecoder`: 在恢复时解码两个非指针数据字
- `NewInlineCompletion(token Token, word0, word1 uint64, decoder CompletionDecoder, err error) Completion`: 构造内联完成事件
- `NewLoop[B, R](b B, capacity int, opts ...Option) *Loop[B, R]`: 创建事件循环（默认使用 `HeapMemory` 与默认尺寸 slab）
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)`: 预留容量后求值；立即返回结果或将挂起入队
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` 先预留一个槽位，再调用 `kont.Step(m)`，随后返回结果或将挂起入队。
- `(*Loop[B, R]).Poll() ([]R, error)`: 轮询并派发完成事件
- `(*Loop[B, R]).Run() ([]R, error)`: 驱动至全部完成
- `(*Loop[B, R]).SubmitSuspension(s *kont.Suspension[R]) error`
- `(*Loop[B, R]).Ready() bool`
- `(*Loop[B, R]).Pending() int`: 本地计算链数量，包括延后重试
- `(*Loop[B, R]).Failed() error`: 终端致命错误；未发生时为 nil
- `(*Loop[B, R]).Drain() int`: 丢弃挂起并废弃循环
- `ErrLiveTokenReuse`: 后端复用了一个在循环中仍然存活的 token
- `ErrUnsupportedMultishot`: 通用 `Loop` 不支持多次触发完成
- `ErrDisposed`: 循环已通过 `Drain` 废弃

### 流路由

- `RouteID`: 流路由身份（`Token` 加 generation）
- `NewRouteID(token Token, generation uint64) RouteID`: 为流后端构造路由标识符
- `RouteID.Token() Token`: token 组成部分
- `RouteID.Generation() uint64`: generation 组成部分
- `RouteID.IsZero() bool`: 保留的无效路由测试
- `Subscription[A]`: 不透明的流路由身份
- `Subscription[A].ID() RouteID`: 该句柄携带的路由身份
- `Subscription[A].IsZero() bool`: 零值句柄测试；不查询路由是否存活
- `StreamCompletion[A]`: `{ID, Value, HasValue, EventErr, More}`
- `StreamCompletion[A].RouteOutcome() iox.Outcome`: 投影到 `iox` 结果词汇
- `StreamEvent[A]`: `{Subscription, Value, HasValue, Final, EventErr}`
- `SubscriptionBackend[B, A]`: 抽象流后端接口（`Subscribe`, `Poll`, `Cancel`）
- `SubscriptionOption`: `NewSubscriptionLoop` 的函数式选项
- `WithMaxStreamCompletions(n)`: 限制每次轮询的流完成事件缓冲区
- `NewSubscriptionLoop[B, A](b B, capacity int, opts ...SubscriptionOption) *SubscriptionLoop[B, A]`: 创建流运行器
- `(*SubscriptionLoop[B, A]).Subscribe(op kont.Operation) (Subscription[A], error)`: 启动产生路由的操作
- `(*SubscriptionLoop[B, A]).Poll() ([]StreamEvent[A], error)`: 轮询按路由索引的事件
- `(*SubscriptionLoop[B, A]).Cancel(sub Subscription[A]) error`: 请求取消路由
- `(*SubscriptionLoop[B, A]).Pending() int`: 存活流路由数量
- `(*SubscriptionLoop[B, A]).Failed() error`: 终端致命错误；未发生时为 nil
- `(*SubscriptionLoop[B, A]).Drain() int`: 取消或退休已拥有路由，并废弃流循环
- `ErrLiveRouteReuse`: 后端复用了仍然存活的路由
- `ErrInvalidRouteID`: 后端返回了保留的零 `RouteID`
- `ErrUnknownSubscription`: 订阅句柄在流循环中并非存活

### 桥接

- `Reify[A](kont.Eff[A]) kont.Expr[A]`: Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]`: Expr → Eff

## 实用范式

`Loop` 使用 `Backend`，`Exec` 和 `Advance` 使用 `Dispatcher`。它们是独立执行路径；Loop 不会自动调用 Dispatcher。`ExecError` 和 `AdvanceError` 显式解释应用错误效果。原始 Loop 需要后端或解释器处理所有已接纳的操作，包括错误效果。[可运行示例](example_test.go)展示了后继处理、立即返回结果和延后提交后端。

[验证指南](docs/verification.md)介绍检查命令和证明范围。[性能指南](docs/performance.md)涵盖局部性、调度、自适应退避、类型化分派和可重复的基准测试。

## 参考文献

- Tarmo Uustalu and Varmo Vene. 2008. Comonadic Notions of Computation. *Electronic Notes in Theoretical Computer Science* 203, 5 (June 2008), 263–284. https://doi.org/10.1016/j.entcs.2008.05.029
- Gordon D. Plotkin and Matija Pretnar. 2009. Handlers of Algebraic Effects. In *Proc. 18th European Symposium on Programming (ESOP '09)*. LNCS 5502, 80–94. https://doi.org/10.1007/978-3-642-00590-9_7
- Andrej Bauer and Matija Pretnar. 2015. Programming with Algebraic Effects and Handlers. *Journal of Logical and Algebraic Methods in Programming* 84, 1 (Jan. 2015), 108–123. https://arxiv.org/abs/1203.1539
- Daniel Leijen. 2017. Type Directed Compilation of Row-Typed Algebraic Effects. In *Proc. 44th ACM SIGPLAN Symposium on Principles of Programming Languages (POPL '17)*. 486–499. https://doi.org/10.1145/3009837.3009872
- Danel Ahman and Andrej Bauer. 2020. Runners in Action. In *Proc. 29th European Symposium on Programming (ESOP '20)*. LNCS 12075, 29–55. https://arxiv.org/abs/1910.11629
- Daniel Hillerström, Sam Lindley, and Robert Atkey. 2020. Effect Handlers via Generalised Continuations. *Journal of Functional Programming* 30 (2020), e5. https://bentnib.org/handlers-cps-journal.pdf

## 许可证

MIT 许可证。详见 [LICENSE](LICENSE)。

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
