[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Benchmarks](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml/badge.svg)](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | [Español](README.es.md) | **日本語** | [Français](README.fr.md)

# takt

ノンブロッキング I/O スタック向けの抽象的な完了イベント駆動ディスパッチエンジン。

## 概要

Proactor モデルでは、バックエンドが I/O 要求を受け付け、完了を非同期に通知します。完了通知が処理中の要求に対応し、書き込んだバイト数などの有効な応答を含んでいれば、ループはその応答を待っている計算を再開します。

`takt` はこの実行モデルを [kont](https://code.hybscloud.com/kont) エフェクトシステム上の抽象レイヤーとして提供します。`Dispatcher` は一度に 1 つの代数的エフェクトを評価します。`Advance` は [iox](https://code.hybscloud.com/iox) のアウトカム代数に従って結果を分類します。`Backend` は非同期エンジン、たとえば `io_uring` に操作を送信し、完了をポーリングします。`Loop` はバックエンドに操作を送信し、バックエンドをポーリングし、トークンで完了を関連付け、中断された継続を再開します。

プログラムには 2 つの表現があります。`kont.Eff` は合成にクロージャを使い、`kont.Expr` はステップ実行用のフレームを公開します。`Reify` と `Reflect` が両者を接続します。割り当てと実行のコストはプログラムと実現方法に依存し、表現の選択だけではホットパスの低コストを保証しません。

イベントループ経路は、生存中の各トークンにつき 1 つの保留サスペンションだけを保持します。各トークンは `kont.StepExpr`（または事前に `kont.Eff` を reify したもの）から生じたサスペンションを追跡するため、`Backend.Submit` はそのトークンを持つ古い送信がループ内でまだ生きている間は、そのトークンを再利用してはいけません。

ストリームまたは multishot 形式の統合には、`SubscriptionLoop` が別個の抽象ルートランナーを提供します。`RouteID`（`Token` と generation）で `Subscription` ハンドルを追跡し、`StreamCompletion` 値をポーリングし、`StreamEvent` 観測を発行し、`More` をペイロード値やペイロードエラーから独立したルート生存証拠として保持します。これにより汎用 `Loop` はアフィンで一回限りのまま保たれ、具体ランタイムは同一操作の後続観測を表す明確な場所を持てます。

## 合成境界

`takt` が所有するのは実行の移動であり、コンテキストの意味やアウトカム語彙ではありません。`iox` は `nil`、`ErrWouldBlock`、`ErrMore`、失敗を分類します。`kont` はサスペンション/再開キャリアを所有します。`cove` は `SuspensionView` によってサスペンションを明示的なコンテキストで包めます。`takt` は `SuspensionLike` を満たす値を進めますが、そのコンテキストは解釈しません。

## インストール

```bash
go get code.hybscloud.com/takt
```

Go 1.27 以降が必要です。

## アウトカム分類

ディスパッチされた各操作は `iox` アウトカムを返します。`Dispatcher.Dispatch` はそのアウトカムを `(value, error)` として報告し、ブロッキングランナーとステッピング API は次のように解釈します:

| アウトカム           | 意味            | Dispatcher の戻り値         | ブロッキング/ステッピング動作                         |
|-----------------|---------------|--------------------------|-------------------------------------------|
| `nil`           | 有効な応答 | `(value, nil)`           | 再開                                        |
| `ErrMore`       | 有効な応答；処理は継続中 | `(value, ErrMore)`       | 再開し、ステッピングでは `ErrMore` を返す                 |
| `ErrWouldBlock` | 現在は応答なし；後で再試行 | `(nil, ErrWouldBlock)`   | ブロッキングは待機し、ステッピングはサスペンションを返す           |
| その他             | インフラ障害        | `(nil, error)`           | ブロッキングは panic し、ステッピングはエラーを返す             |

この表は Dispatcher の応答契約を示します。`iox` の制御値だけでは転送バイト数やリソース解放を判断できません。WouldBlock を返す際、部分的な仕事は dispatcher が保持します。

ディスパッチはラップされたエラーを含めて `iox.Classify` に従います。`ErrWouldBlock` と `ErrMore` の両方に一致するエラーは WouldBlock と分類され、サスペンションを保持して後で再試行します。

## 使い方

### ディスパッチャ

`Dispatcher` は各代数的エフェクトを具体的な I/O 操作にマッピングし、`iox` アウトカムとともに結果を返します。

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	// op をディスパッチし、(value, nil)、(value, iox.ErrMore)、または (nil, iox.ErrWouldBlock) を返す
}
```

### ブロッキング評価

`Exec` と `ExecExpr` は計算を完了まで実行し、ディスパッチャが `iox.ErrWouldBlock` を返すと同期的に待機します。

```go
result := takt.Exec(d, computation) // kont.Eff
```

```go
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### ステッピング

`Step` は、式が値を返すか、操作の応答を待って中断するまで評価を進めます。`Advance` はその操作をディスパッチャに直接渡します。ディスパッチャがデータの一部を書き込んでから `iox.ErrWouldBlock` を返した場合、ディスパッチャは書き込み位置を保持し、呼び出し元は同じサスペンションを保持して再試行します。

手動ステッピングのアウトカム規則は明示的です。`d.Dispatch(susp.Op())` が `nil` または `iox.ErrMore` を返した場合、`Advance` は返された値でサスペンションを再開し、次のサスペンションと元の error を返します。`iox.ErrWouldBlock` または通常の失敗を返した場合、`Advance` はゼロ値の結果、元のサスペンション、その error を返します。したがって `ErrMore` は手動ステッピングでは進行シグナルですが、完了レベルの `ErrMore` はバックエンド操作がまだ生きているため、汎用 `Loop` ではサポートされません。

```go
result, susp := takt.Step[int](exprComputation)
var err error
if susp != nil {
	result, susp, err = takt.Advance(d, susp)
}
// susp == nil のときだけ result は最終値です。それ以外は susp を保持し、次に進める前に err を処理します。
```

保留中の操作に必要な許可が取り消される可能性がある場合は、現在のコンテキストで `cove.CheckSuspension` を呼びます。条件を満たさなければ元のサスペンションを保持します。条件を満たせばビューを `AdvanceSuspension` に渡し、呼び出しが終わるまで許可が有効であることを保証します。`iox.ErrWouldBlock` が返ったら、次の関連イベントで許可を再確認します。[書き込み許可を確認する例](write_session_example_test.go)

### エラー処理

`Throw` のエラー型が対象の型と一致すると、`AdvanceError` は保留中のサスペンションを破棄し、`Left(e)` を返します。

```go
either := takt.ExecError[string](d, computation)
// Right on success, Left on Throw

either, susp := takt.StepError[string, int](exprComputation)
var err error
if susp != nil {
	either, susp, err = takt.AdvanceError[string](d, susp)
}
// susp == nil のときだけ either は最終値です。WouldBlock や基盤エラー時もサスペンションを保持します。
```

`cove.SuspensionView[C, kont.Either[E, A]]` のサスペンションが保留中で条件を満たしていれば、`either, view.Suspension, err = takt.AdvanceError(d, view.Suspension)` で進めます。`view.Suspension == nil` になると `either` が最終結果となり、`view.Ctx` は引き続き参照できます。

### イベントループ

`Loop` と `SubscriptionLoop` へのすべてのアクセスを直列化してください。`Ready`、`Pending`、`Failed` も対象です。コールバックから状態を変更するメソッドへ再入してはいけません。構築後のループを値コピーしてはいけません。

`NewLoop` と `NewSubscriptionLoop` には正の容量を指定します。`Submit` と `SubmitExpr` はローカルで処理を受け入れ、`Poll` がバックエンドへ投入します。容量不足による拒否ではプログラムの先頭部分も実行しません。`SubmitSuspension` は成功時だけ所有権を移します。`Ready` は新しい処理と延期された再試行を区別します。バッチ全体の処理後に継続を再開し、後続の投入は次の巡回で行います。ローカルな破棄はネイティブ資源の解放を意味しません。[実行契約](docs/execution.md)と[証明の範囲](proofs/README.md)を参照してください。

純粋な計算は即座に完了する場合があります。`Submit` や `SubmitExpr` の `done` と結果を確認してください。`WithMaxCompletions` は送信試行、観測、再開の各段階を個別に制限します。

`Backend.Poll([]Completion) (int, error)` は、準備済み完了件数とインフラストラクチャのポーリング失敗の両方を報告します。バックエンドは、非 nil のポーリングレベル error と準備済み完了を同時に返してはいけません。`Loop` は `Poll` から返る `iox.ErrWouldBlock` を終端エラーではなくアイドルティックとして扱います。

完了が成功または通常の失敗を示す場合、`Loop` は操作エラーを含む有効な応答を `Completion.ResumeValue()` で取得して再開します。通常のバックエンドは `Value` を設定します。ポインタを含まない固定サイズのペイロードには `NewInlineCompletion` と `CompletionDecoder` を使い、インターフェースへの格納を再開時まで遅らせることができます。`Value` を直接読んでもインラインペイロードはデコードされません。デコード時にはメモリ割り当てが発生する場合があります。

`Loop` は単一所有者のランナーです。同じ `Loop` を共有する `SubmitExpr`, `Submit`, `Poll`, `Run`, `Drain`, `Pending`, `Failed` の呼び出しは直列化してください。

`Backend.Submit` は、ループ内でまだ生存しているすべての送信の間で一意なトークンを返さなければなりません。トークンは相関キーであり、シーケンス番号ではありません。具体的なバックエンドは kernel `user_data` 値を直接使えます。バックエンドが生存中トークンを再利用した場合、ループは `ErrLiveTokenReuse` を記録し、すべての保留サスペンションをちょうど一度だけ破棄し、その後の `SubmitExpr` / `Submit` / `Poll` / `Run` はこの致命的エラーを返します。

`iox.ErrWouldBlock` を伴う完了は、同じサスペンションを次のラウンドへ延期します。旧リクエストが応答や隠れた部分進行を残さず終了していることが前提です。完了レベルの `iox.ErrMore` は `ErrUnsupportedMultishot` を記録します。対応する処理はまだ生存しており、購読の所有者が必要です。ループはバッチ全体を処理してから再開します。障害前の安定した応答は完了できますが、その後続と残りのサスペンションはバインディングの放棄契約に従って破棄されます。`Poll` は完了した前半の結果を先に返し、保存したエラーを後の呼び出しで返します。`Run` は収集した結果とエラーを同時に返します。

`Loop.Poll` の結果は次の `Poll`、`Run`、`Drain` まで、ストリームイベントは次の `Poll`、`Drain` までの借用です。保持する値は先にコピーしてください。借用終了時には、使われなくなったバッファ末尾を含めランナー内の参照を消去します。参照の消去はリソース解放ではありません。ネイティブ要求、記述子、バッファの借用にはそれぞれの所有者が残ります。完了後や障害後も `Loop.Drain` を呼び、完了スラブを返却してください。

`Loop.Failed()` は記録された致命的エラー（または `nil`）を返します。`Loop.Drain()` はループを廃棄状態へ強制し、保留中のサスペンションをそれぞれ一度だけ破棄し、致命的エラーが未設定の場合に限って `ErrDisposed` を記録します。冪等であり、既存の致命的エラーは保持されます。

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

### ストリーム / サブスクリプションランナー

`SubscriptionLoop` は、ルートでインデックスされたストリーム観測のための同格ランナーです。`SubscriptionBackend` は `Subscribe` で操作を開始し、`StreamCompletion` 値をポーリングし、生存中ルートに対する `Cancel` 要求を受け付けます。すべてのルート枠が使用中なら、`Subscribe` はバックエンドを呼ぶ前に `iox.ErrWouldBlock` を返します。`Subscription` は発行元のループで使用します。古いハンドルや完了通知がそのループに届く可能性がある間は、バックエンドが `Token` を再利用する際に `RouteID` の世代を更新する必要があります。

`StreamCompletion.More` は、その観測後も同じルートが生存していることを意味します。`HasValue` と `EventErr` はその境界におけるペイロード証拠を記述し、`More` とは独立しています。つまり完了は、値と後続あり、値なしで後続あり、またはルートが生存したままのペイロードエラーを表せます。`More == false` の完了は final `StreamEvent` を発行し、そのルートを退役させます。

`Subscribe` はゼロ `RouteID` を `ErrInvalidRouteID` で拒否し、生存中ルートの別名化を `ErrLiveRouteReuse` で拒否します。どちらの条件もストリームループを致命状態にします。ポーリングレベルの `iox.ErrWouldBlock` はアイドルティックです。`SubscriptionBackend` は、非 nil のポーリングレベル error と準備済みストリーム完了を同時に返してはいけません。ペイロードエラーは `StreamCompletion.EventErr` に載せます。未知またはすでに退役したルート完了は古い観測として無視されます。`Cancel` はルートキャンセルを要求しますが、即座には退役させません。終端完了、`Drain`、または致命的なループ遷移がそのルートを退役させます。

```go
type myStreamBackend struct{ /* ... */ }

func (b *myStreamBackend) Subscribe(op kont.Operation) (takt.RouteID, error) {
	return takt.NewRouteID(tok, generation), nil
}

func (b *myStreamBackend) Poll(out []takt.StreamCompletion[int]) (int, error) {
	// ルートでインデックスされた観測を out に書き込む。
	return n, nil
}

func (b *myStreamBackend) Cancel(id takt.RouteID) error {
	// 生存中ルートのキャンセルを要求する。
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

### 完了バッファのメモリ

`NewLoop` は完了バッファの既定プロバイダとして [`HeapMemory`](completion_memory.go) を使います。完了バッファを既定サイズ 128 KiB の slab から成る有界かつ安定したプールから取得したい場合は、[`WithMemory`](option.go) で [`BoundedMemory`](completion_memory.go) を渡してください。あるいは、`Backend` や `Completion` の契約を広げずに割り当て戦略を制御したい場合は、独自の `CompletionMemory` を実装してください。カスタムプロバイダは、生存中 slab を他と重ならない排他的なものとして返し、`Release` をプロバイダ側への所有権移譲として扱ってかまいません:

```go
// 既定: HeapMemory + 既定サイズの完了バッファ。
defaultLoop := takt.NewLoop[*myBackend, int](backend, 128)
defer defaultLoop.Drain()

// 1 回のポーリングで扱う完了バッファ長に上限を設ける（プロバイダがバッファ形状を決め、Loop が可視長をこの上限まで切り詰める）。
cappedLoop := takt.NewLoop[*myBackend, int](backend, 128, takt.WithMaxCompletions(64))
defer cappedLoop.Drain()

// 同じ HeapMemory ポインタをループ間で共有します。初回使用後は HeapMemory や BoundedMemory をコピーしてはいけません。
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backendA, 128, takt.WithMemory(heap))
defer loopA.Drain()
loopB := takt.NewLoop[*myBackend, int](backendB, 128, takt.WithMemory(heap))
defer loopB.Drain()

// BoundedMemory: 既定サイズ 128 KiB の slab を保持する単一の有界プール。WithPoolCapacity でそのプール容量を調整する（iobuf が次の 2 の冪に切り上げる）。
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
boundedLoop := takt.NewLoop[*myBackend, int](
	backend, 128,
	takt.WithMemory(bounded),
	takt.WithMaxCompletions(64),
)
defer boundedLoop.Drain()
```

`BoundedMemory` が制限するのはプールに保持するストレージであり、使用中のメモリ総量ではありません。プールが枯渇すると既定サイズの slab を追加で割り当て、それを超えるサイズの要求には要求どおりのサイズを割り当てます。`Release` はこれらの追加 slab を保持しません。計算の完了後や障害発生後も、各ループの `Drain` を呼んでストレージを返却してください。ネイティブリソースの解放は引き続きバインディングが担います。

## API 概要

### ディスパッチ

- `Dispatcher[D Dispatcher[D]]`: ノンブロッキングなディスパッチインターフェース
- `Exec[D, R](d D, m kont.Eff[R]) R`: `kont.Eff` のブロッキング評価
- `ExecExpr[D, R](d D, m kont.Expr[R]) R`: `kont.Expr` のブロッキング評価

### ステッピング

- `SuspensionLike[S, R]`: `Op` と `Resume` を備えた再開可能インターフェース。`cove.SuspensionView` がこれを実装する
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])`: 最初のサスペンションまで評価する
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)`: 任意の `SuspensionLike` 値に対して 1 操作をディスパッチする
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)`: 1 操作をディスパッチする

### エラー処理

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]`: エラー付きのブロッキング評価
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]`: `kont.Expr` のエラー付き評価
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])`: エラー付きステッピング
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)`: エラー付きで 1 ステップ進める

### バックエンドとイベントループ

- `Backend[B Backend[B]]`: 非同期 submit/poll インターフェース
- `CompletionMemory`: ループローカルな完了バッファプロバイダ
- `HeapMemory`: 既定実装（`sync.Pool` ベースの既定サイズ付き型付き slab）
- `BoundedMemory`: iobuf ベースの実装。既定サイズ 128 KiB の slab を保持する単一の有界プールを持つ
- `Option`: `NewLoop` 用の関数型オプション（`WithMemory`, `WithMaxCompletions`）
- `CompletionBufOption`: `CompletionMemory.CompletionBuf` 用の関数型オプション（`WithSize`）
- `BoundedMemoryOption`: `NewBoundedMemory` 用の関数型オプション（`WithPoolCapacity`）
- `Token`: 送信と完了の対応付け（`uint64`）
- `Completion`: トークン、結果状態、直接またはインライン形式の応答ペイロード
- `Completion.ResumeValue() kont.Resumed`: どちらの表現からも応答を取得する
- `CompletionDecoder`: 再開時にポインタを含まない 2 ワードをデコードする
- `NewInlineCompletion(token Token, word0, word1 uint64, decoder CompletionDecoder, err error) Completion`: インライン完了を構築する
- `NewLoop[B, R](b B, capacity int, opts ...Option) *Loop[B, R]`: イベントループを作成する（既定では `HeapMemory` と既定サイズの slab を使用）
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)`: 容量確保後に評価；即時の結果またはキュー内のサスペンション
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)`: 容量確保後に reify；即時の結果またはキュー内のサスペンション
- `(*Loop[B, R]).Poll() ([]R, error)`: 完了をポーリングしてディスパッチする
- `(*Loop[B, R]).Run() ([]R, error)`: すべて完了するまで進める
- `(*Loop[B, R]).SubmitSuspension(s *kont.Suspension[R]) error`
- `(*Loop[B, R]).Ready() bool`
- `(*Loop[B, R]).Pending() int`: 延期された再試行を含むローカル計算系列の数
- `(*Loop[B, R]).Failed() error`: 終端致命エラー。未発生時は nil
- `(*Loop[B, R]).Drain() int`: 保留中のサスペンションを破棄してループを廃棄する
- `ErrLiveTokenReuse`: バックエンドがループ内でまだ生存しているトークンを再利用したことを示す
- `ErrUnsupportedMultishot`: 汎用 `Loop` はマルチショット完了をサポートしない
- `ErrDisposed`: `Drain` によりループが廃棄されたことを示す

### ストリームルート

- `RouteID`: ストリームルートの識別情報（`Token` と generation）
- `NewRouteID(token Token, generation uint64) RouteID`: ストリームバックエンド用のルート識別子を作成する
- `RouteID.Token() Token`: `Token` 成分
- `RouteID.Generation() uint64`: generation 成分
- `RouteID.IsZero() bool`: 予約済み無効ルートの判定
- `Subscription[A]`: 不透明なストリームルートの識別情報
- `Subscription[A].ID() RouteID`: ハンドルが保持するルート識別情報
- `Subscription[A].IsZero() bool`: ゼロ値ハンドルの判定。ルートの生存状態は照会しない
- `StreamCompletion[A]`: `{ID, Value, HasValue, EventErr, More}`
- `StreamCompletion[A].RouteOutcome() iox.Outcome`: `iox` アウトカム語彙への射影
- `StreamEvent[A]`: `{Subscription, Value, HasValue, Final, EventErr}`
- `SubscriptionBackend[B, A]`: 抽象ストリームバックエンドインターフェース（`Subscribe`, `Poll`, `Cancel`）
- `SubscriptionOption`: `NewSubscriptionLoop` 用の関数型オプション
- `WithMaxStreamCompletions(n)`: 1 回のポーリングに使うストリーム完了バッファを制限する
- `NewSubscriptionLoop[B, A](b B, capacity int, opts ...SubscriptionOption) *SubscriptionLoop[B, A]`: ストリームランナーを作成する
- `(*SubscriptionLoop[B, A]).Subscribe(op kont.Operation) (Subscription[A], error)`: ルート生成操作を開始する
- `(*SubscriptionLoop[B, A]).Poll() ([]StreamEvent[A], error)`: ルートでインデックスされたイベントをポーリングする
- `(*SubscriptionLoop[B, A]).Cancel(sub Subscription[A]) error`: ルートキャンセルを要求する
- `(*SubscriptionLoop[B, A]).Pending() int`: 生存中ストリームルート数
- `(*SubscriptionLoop[B, A]).Failed() error`: 終端致命エラー。未発生時は nil
- `(*SubscriptionLoop[B, A]).Drain() int`: 所有中ルートをキャンセルまたは退役させ、ストリームループを廃棄する
- `ErrLiveRouteReuse`: バックエンドがまだ生存中のルートを再利用したことを示す
- `ErrInvalidRouteID`: バックエンドが予約済みゼロ `RouteID` を返したことを示す
- `ErrUnknownSubscription`: サブスクリプションハンドルがストリームループ内で生存していないことを示す

### ブリッジ

- `Reify[A](kont.Eff[A]) kont.Expr[A]`: Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]`: Expr → Cont

## 実用レシピ

`Loop` では `Backend`、`Exec` と `Advance` では `Dispatcher` を使います。これらは別の実行経路であり、Loop が自動的に Dispatcher を呼ぶわけではありません。`ExecError` と `AdvanceError` はアプリケーションのエラー効果を明示的に解釈します。素の Loop では、エラー効果を含むすべての受け入れ操作を扱えるバックエンドまたはインタプリタが必要です。[実行可能な例](example_test.go)では、後続サスペンション、即時の結果、バックエンドへの遅延送信を示します。

[検証ガイド](docs/verification.md)には検証コマンドと証明の適用範囲を記載しています。[性能ガイド](docs/performance.md)では局所性、スケジューリング、適応的バックオフ、型付きディスパッチ、再現可能な測定方法を説明しています。

## 参考文献

- Tarmo Uustalu and Varmo Vene. 2008. Comonadic Notions of Computation. *Electronic Notes in Theoretical Computer Science* 203, 5 (June 2008), 263–284. https://doi.org/10.1016/j.entcs.2008.05.029
- Gordon D. Plotkin and Matija Pretnar. 2009. Handlers of Algebraic Effects. In *Proc. 18th European Symposium on Programming (ESOP '09)*. LNCS 5502, 80–94. https://doi.org/10.1007/978-3-642-00590-9_7
- Andrej Bauer and Matija Pretnar. 2015. Programming with Algebraic Effects and Handlers. *Journal of Logical and Algebraic Methods in Programming* 84, 1 (Jan. 2015), 108–123. https://arxiv.org/abs/1203.1539
- Daniel Leijen. 2017. Type Directed Compilation of Row-Typed Algebraic Effects. In *Proc. 44th ACM SIGPLAN Symposium on Principles of Programming Languages (POPL '17)*. 486–499. https://doi.org/10.1145/3009837.3009872
- Danel Ahman and Andrej Bauer. 2020. Runners in Action. In *Proc. 29th European Symposium on Programming (ESOP '20)*. LNCS 12075, 29–55. https://arxiv.org/abs/1910.11629
- Daniel Hillerström, Sam Lindley, and Robert Atkey. 2020. Effect Handlers via Generalised Continuations. *Journal of Functional Programming* 30 (2020), e5. https://bentnib.org/handlers-cps-journal.pdf

## ライセンス

MIT ライセンス。詳細は [LICENSE](LICENSE) を参照してください。

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
