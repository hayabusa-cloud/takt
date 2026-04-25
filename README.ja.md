[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | [Español](README.es.md) | **日本語** | [Français](README.fr.md)

# takt

ノンブロッキング I/O スタック向けの抽象的な完了イベント駆動ディスパッチエンジン。

## 概要

proactor モデルでは、I/O
操作はカーネルに送信され、その完了は非同期に到着します。アプリケーションは各完了をそれを要求した計算に対応付け、その計算を再開し、結果の全体 —
成功、部分的な進行、バックプレッシャー、失敗 — を処理しなければなりません。

`takt` はこの実行モデルを [kont](https://code.hybscloud.com/kont) エフェクトシステム上の抽象レイヤーとして提供します。
`Dispatcher` は一度に 1 つの代数的エフェクトを評価し、[iox](https://code.hybscloud.com/iox) のアウトカム代数に従って結果を分類します。
`Backend` は非同期エンジン（例: `io_uring`）に操作を送信し、完了をポーリングします。`Loop`
はそれらをまとめ、計算を送信し、バックエンドをポーリングし、トークンで完了を関連付け、中断された継続を再開します。

等価な API が 2 つあります。`kont.Eff` はクロージャベースで合成しやすく、`kont.Expr` はフレームベースでホットパスの割り当てを抑えます。

イベントループ経路は、生存中の各トークンにつき 1 つの保留サスペンションだけを保持します。各トークンは `kont.StepExpr`
（または事前に `kont.Eff` を reify したもの）から生じたサスペンションを追跡するため、`Backend.Submit`
はそのトークンを持つ古い送信がループ内でまだ生きている間は、そのトークンを再利用してはいけません。

## インストール

```bash
go get code.hybscloud.com/takt
```

Go 1.26 以降が必要です。

## アウトカム分類

ディスパッチされた各操作は `iox` アウトカムを返します。ディスパッチャとステッピング API は各ケースを次のように扱います:

| アウトカム           | 意味            | ディスパッチャ | ステッピング API        |
|-----------------|---------------|---------|-------------------|
| `nil`           | 完了            | 再開      | 再開し、`nil` を返す     |
| `ErrMore`       | 進行あり、続きが期待される | 再開      | 再開し、`ErrMore` を返す |
| `ErrWouldBlock` | 進行なし          | 待機      | サスペンションを呼び出し元に返す  |
| その他             | インフラ障害        | panic   | エラーを呼び出し元に返す      |

## 使い方

### ディスパッチャ

`Dispatcher` は各代数的エフェクトを具体的な I/O 操作にマッピングし、`iox` アウトカムとともに結果を返します。

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
// op をディスパッチし、(value, nil) または (nil, iox.ErrWouldBlock) を返す
}
```

### ブロッキング評価

`Exec` と `ExecExpr` は計算を完了まで実行し、ディスパッチャが `iox.ErrWouldBlock` を返すと同期的に待機します。

```go
result := takt.Exec(d, computation) // kont.Eff
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### ステッピング

proactor イベントループ（例: `io_uring`）向けに、`Step` と `Advance` は一度に 1 つのエフェクトを評価します。ディスパッチャが
`iox.ErrWouldBlock` を返すと、サスペンションが呼び出し元に返され、イベントループが再スケジュールできるようになります。

```go
result, susp := takt.Step[int](exprComputation)
if susp != nil {
    var err error
    result, susp, err = takt.Advance(d, susp)
    if iox.IsWouldBlock(err) {
        return susp // イベントループへ制御を戻し、準備できたら再スケジュールする
    }
}
// result が最終結果
```

### エラー処理

ディスパッチャ操作をエラーエフェクトと合成できます。`Throw` は計算を即座に短絡し、保留中のサスペンションを破棄します。

```go
either := takt.ExecError[string](d, computation)
// 成功時は Right、Throw 時は Left

// エラー付きステッピング
either, susp := takt.StepError[string, int](exprComputation)
if susp != nil {
    var err error
    either, susp, err = takt.AdvanceError[string](d, susp)
    if iox.IsWouldBlock(err) {
        return susp // イベントループへ制御を戻し、準備できたら再スケジュールする
    }
}
```

### イベントループ

`Loop` は `Backend` を通じて計算を駆動します。操作を送信し、完了をポーリングし、`Token` で関連付け、中断された継続を再開します。
`NewLoop` は関数型 `Option` を受け取ります。`WithMaxCompletions(n)` は `n <= 0` のとき
`takt: WithMaxCompletions requires n > 0` でパニックし、`WithMemory(nil)` は
`takt: WithMemory requires a non-nil CompletionMemory` でパニックします。

`Backend.Poll([]Completion) (int, error)` は、準備済み完了件数とインフラストラクチャのポーリング失敗の両方を報告します。
`Loop` は `Poll` から返る `iox.ErrWouldBlock` を終端エラーではなくアイドルティックとして扱います。

`Backend.Submit` は、ループ内でまだ生存しているすべての送信の間で一意なトークンを返さなければなりません。バックエンドが生存中トークンを再利用した場合、ループは
`ErrLiveTokenReuse` を記録し、すべての保留サスペンションをちょうど一度だけ破棄し、その後の `SubmitExpr` / `Submit` /
`Poll` / `Run` はこの致命的エラーを返します。

完了が `iox.ErrWouldBlock` を伴う場合、ループは同じ操作を再送信します。`iox.ErrMore`
（マルチショット）完了が新しいサスペンドされたエフェクトに再開しようとする場合、ループは `ErrUnsupportedMultishot`
を記録し、保留中のサスペンションをそれぞれ一度だけ破棄し、以降の `SubmitExpr` / `Submit` / `Poll` / `Run`
呼び出しはその致命的エラーを返します。
この拒否はループのトークンとサスペンションの対応を保ちます。マルチショット系列は現在のサスペンションを再開し続けられても、以前の送信の下で新しい保留エフェクトを作ることはできません。

`Loop.Failed()` は記録された致命的エラー（または `nil`）を返します。`Loop.Drain()`
はループを廃棄状態へ強制し、保留中のサスペンションをそれぞれ一度だけ破棄し、致命的エラーが未設定の場合に限って
`ErrDisposed` を記録します。冪等であり、既存の致命的エラーは保持されます。

```go
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// 計算を送信する
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // kont.Eff

// すべて完了するまで進める
results, err := loop.Run()
```

`NewLoop` は完了バッファの既定プロバイダとして [`HeapMemory`](completion_memory.go) を使います。完了バッファを既定サイズ
128 KiB の slab から成る有界かつ安定したプールから取得したい場合は、[`WithMemory`](option.go) で [
`BoundedMemory`](completion_memory.go) を渡してください。あるいは、`Backend` や `Completion` の契約を広げずに割り当て戦略を制御したい場合は、独自の
`CompletionMemory` を実装してください。カスタムプロバイダは、生存中 slab を他と重ならない排他的なものとして返し、`Release`
をプロバイダ側への所有権移譲として扱ってかまいません:

```go
// 既定: HeapMemory + 既定サイズの完了バッファ。
loop := takt.NewLoop[*myBackend, int](backend)

// 1 回のポーリングで扱う完了バッファ長に上限を設ける（プロバイダがバッファ形状を決め、Loop が可視長をこの上限まで切り詰める）。
loop = takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// 同じ HeapMemory のポインタを WithMemory に渡すことで、複数の Loop 間で sync.Pool を共有する。HeapMemory を値コピーすると再利用されたバッファは共有されない。
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))
loopB := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))

// BoundedMemory: 既定サイズ 128 KiB の slab を保持する単一の有界プール。WithPoolCapacity でそのプール容量を調整する（iobuf が次の 2 の冪に切り上げる）。
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
loop = takt.NewLoop[*myBackend, int](
    backend,
    takt.WithMemory(bounded),
    takt.WithMaxCompletions(64),
)
```

## API 概要

### ディスパッチ

- `Dispatcher[D Dispatcher[D]]` — ノンブロッキングなディスパッチインターフェース
- `Exec[D, R](d D, m kont.Eff[R]) R` — `kont.Eff` のブロッキング評価
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — `kont.Expr` のブロッキング評価

### ステッピング

- `SuspensionLike[S, R]` — `Op` と `Resume` を備えた再開可能インターフェース。`cove.SuspensionView` がこれを実装する
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — 最初のサスペンションまで評価する
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)` — 任意の `SuspensionLike` 値に対して 1 操作をディスパッチする
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — 1 操作をディスパッチする

### エラー処理

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — エラー付きのブロッキング評価
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — `kont.Expr` のエラー付き評価
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — エラー付きステッピング
-
`AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` —
エラー付きで 1 ステップ進める

### バックエンドとイベントループ

- `Backend[B Backend[B]]` — 非同期 submit/poll インターフェース
- `CompletionMemory` — ループローカルな完了バッファプロバイダ
- `HeapMemory` — 既定実装（`sync.Pool` ベースの既定サイズ付き型付き slab）
- `BoundedMemory` — iobuf ベースの実装。既定サイズ 128 KiB の slab を保持する単一の有界プールを持つ
- `Option` — `NewLoop` 用の関数型オプション（`WithMemory`, `WithMaxCompletions`）
- `CompletionBufOption` — `CompletionMemory.CompletionBuf` 用の関数型オプション（`WithSize`）
- `BoundedMemoryOption` — `NewBoundedMemory` 用の関数型オプション（`WithPoolCapacity`）
- `Token` — 送信と完了の対応付け（`uint64`）
- `Completion` — `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, opts ...Option) *Loop[B, R]` — イベントループを作成する（既定では `HeapMemory` と既定サイズの slab
  を使用）
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)` — 1 ステップ進めて `kont.Expr` を送信する
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` — 1 ステップ進めて `kont.Eff` を送信する
- `(*Loop[B, R]).Poll() ([]R, error)` — 完了をポーリングしてディスパッチする
- `(*Loop[B, R]).Run() ([]R, error)` — すべて完了するまで進める
- `(*Loop[B, R]).Pending() int` — 保留中の操作数
- `(*Loop[B, R]).Failed() error` — 終端致命エラー。未発生時は nil
- `(*Loop[B, R]).Drain() int` — 保留中のサスペンションを破棄してループを廃棄する
- `ErrLiveTokenReuse` — バックエンドがループ内でまだ生存しているトークンを再利用したことを示す
- `ErrUnsupportedMultishot` — マルチショット完了が新しいエフェクトでサスペンドできない
- `ErrDisposed` — `Drain` によりループが廃棄されたことを示す

### ブリッジ

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Cont

## 実用レシピ

完全なイベントループ統合では、`Dispatcher`（同期セマンティクス）と `Backend`（プロアクター）を 1 つの `Loop`
の下に組み合わせます:

```go
// 1. dispatcher を定義する: 効果操作を iox の結果へ写す。
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // 完了時は (value, nil)、譲る必要があるときは (nil, iox.ErrWouldBlock) を返す。
}

// 2. backend を定義する: OS プロアクターに操作を提出し、完了をポーリングする。
type myBackend struct{ /* ... */ }
func (b *myBackend) Submit(op kont.Operation) (takt.Token, error) { /* ... */ }
func (b *myBackend) Poll(out []takt.Completion) (int, error)      { /* ... */ }

// 3. 駆動する: 一つ以上の計算を提出し、Run で完了まで進める。
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))
loop.SubmitExpr(prog1)
loop.SubmitExpr(prog2)
results, err := loop.Run()
_ = results; _ = err
```

エラー付きの合成には `ExecError` / `StepError` / `AdvanceError` を非エラー版の代わりに使います。`Throw`
は進行中の計算を短絡しつつ、同じループ上の他の兄弟計算には影響を与えません。ここで示した dispatcher+backend 融合パターンは、
`sess` がセッションエンドポイントを実 I/O ランタイムに接続するために用いているものと同じです。

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

## ライセンス

MIT ライセンス。詳細は [LICENSE](LICENSE) を参照してください。

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
