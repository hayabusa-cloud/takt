[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | [Español](README.es.md) | **日本語** | [Français](README.fr.md)

# takt

ノンブロッキング I/O スタックのための抽象完了イベント駆動ディスパッチエンジン。

## 概要

proactor モデルでは、I/O 操作はカーネルに送信され、その完了は非同期に到着します。アプリケーションは各完了をそれを要求した計算に関連付け、その計算を再開し、結果の全範囲 — 成功、部分的進行、バックプレッシャー、失敗 — を処理する必要があります。

`takt` はこのディスパッチ代数を [kont](https://code.hybscloud.com/kont) エフェクトシステム上の抽象レイヤーとして提供します。`Dispatcher` は一度に1つの代数的エフェクトを評価し、[iox](https://code.hybscloud.com/iox) アウトカム代数に従って結果を分類します。`Backend` は非同期エンジン（例：`io_uring`）に操作を送信し、完了をポーリングします。`Loop` イベントループはこれらを結合します：計算を送信し、バックエンドをポーリングし、トークンで完了を関連付け、中断された継続を再開します。

2つの等価な API：Cont（クロージャベース、直接的な合成）と Expr（脱関数化、ホットパスで低アロケーション）。

## インストール

```bash
go get code.hybscloud.com/takt
```

Requires Go 1.26+.

## アウトカム分類

ディスパッチされた各操作は `iox` アウトカムを返します。ディスパッチャとステッピング API は各ケースを処理します：

| アウトカム | 意味 | ディスパッチャ | ステッピング API |
-----------|------|--------------|----------------|
 `nil` | 完了 | 再開 | 再開、`nil` を返す |
 `ErrMore` | 進行あり、続きが期待される | 再開 | 再開、`ErrMore` を返す |
 `ErrWouldBlock` | 進行なし | 待機 | サスペンションを呼び出し元に返す |
 その他 | インフラ障害 | panic | エラーを呼び出し元に返す |

## 使い方

### ディスパッチャ

`Dispatcher` は各代数的エフェクトを具体的な I/O 操作にマッピングし、`iox` アウトカムとともに結果を返します。

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // dispatch op, return (value, nil) or (nil, iox.ErrWouldBlock)
}
```

### ブロッキング評価

`Exec` と `ExecExpr` は計算を完了まで実行し、ディスパッチャが `iox.ErrWouldBlock` を返すと同期的に待機します。

```go
result := takt.Exec(d, computation)         // Cont-world
result := takt.ExecExpr(d, exprComputation) // Expr-world
```

### ステッピング

proactor イベントループ（例：`io_uring`）向けに、`Step` と `Advance` は一度に1つのエフェクトを評価します。ディスパッチャが `iox.ErrWouldBlock` を返すと、サスペンションが呼び出し元に返され、イベントループが再スケジュールできるようにします。

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

### エラー処理

ディスパッチャエフェクトをエラーエフェクトと合成します。`Throw` は計算を即座に短絡し、保留中のサスペンションを破棄します。

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

### イベントループ

`Loop` は `Backend` を通じて計算を駆動します。操作を送信し、完了をポーリングし、`Token` で関連付け、中断された継続を再開します。
`NewLoop` の `maxCompletions` は 0 より大きい必要があります。

```go
loop := takt.NewLoop[*myBackend, int](backend, 64)

// Submit computations
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // Cont-world

// Drive all to completion
results, err := loop.Run()
```

## API 概要

### ディスパッチ

- `Dispatcher[D Dispatcher[D]]` — F-bounded dispatch interface
- `Exec[D, R](d D, m kont.Eff[R]) R` — blocking Cont-world evaluation
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — blocking Expr-world evaluation

### ステッピング

- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — evaluate to first suspension
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — dispatch one operation

### エラー処理

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — blocking with errors
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — Expr-world with errors
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — step with errors
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` — advance with errors

### バックエンドとイベントループ

- `Backend[B Backend[B]]` — F-bounded async submit/poll interface
- `Token` — submission-completion correlation (`uint64`)
- `Completion` — `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, maxCompletions int) *Loop[B, R]` — create event loop (`maxCompletions > 0`)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)` — step and submit Expr
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` — step and submit Cont
- `(*Loop[B, R]).Poll() ([]R, error)` — poll and dispatch completions
- `(*Loop[B, R]).Run() ([]R, error)` — drive all to completion
- `(*Loop[B, R]).Pending() int` — count pending operations

### ブリッジ

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Cont

## References

- G. D. Plotkin and M. Pretnar. "Handlers of Algebraic Effects." In *Proc. ESOP*, 2009.
- T. Uustalu and V. Vene. "Comonadic Notions of Computation." In *ENTCS* 203(5), 2008.
- D. Ahman and A. Bauer. "Runners in Action." In *Proc. ESOP*, 2020.

## ライセンス

MIT ライセンス。詳細は [LICENSE](LICENSE) を参照してください。

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
