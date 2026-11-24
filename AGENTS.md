# takt

## Execution semantics

`Step` yields a value or an affine suspension $s=(o,k)$. One driver owns the suspension pointer and may discard it or resume the continuation $k$ once with a valid reply $v$ to $o$. `Advance` and `AdvanceSuspension` dispatch before resuming and return the dispatch error.

```math
\begin{array}{l|ll}
\mathrm{iox.Classify}(\mathit{err}) & \text{direct dispatch error} & \text{matched }\mathtt{Completion.Err}\\ \hline
\mathsf{OK}         & k(v)           & \text{deliver reply in this poll}\\
\mathsf{WouldBlock} & \text{keep }s  & \text{defer }s\\
\mathsf{More}       & k(v)           & \text{record }\mathtt{ErrUnsupportedMultishot}\\
\mathsf{Failure}    & \text{keep }s  & \text{deliver reply in this poll}
\end{array}
```

`Loop.Poll` removes a matched token before classification and skips unknown tokens. `Completion.ResumeValue()` supplies the reply, including any operation failure. A matched WouldBlock permits retry only after the request retires without a reply or hidden partial progress. At `Backend.Poll`, WouldBlock yields an empty batch. Other non-nil poll errors abort the loop. A computation keeps one slot across suspensions, so $\operatorname{Pending}()\le C$ for capacity $C$. `Ready()` reports fresh work or held replies in a healthy loop. [One-shot execution](docs/execution.md#one-shot-computations)

`SubscriptionLoop` emits one event for each completion matching a live `(token, generation)`. It sets $\mathrm{Final}=\neg\mathrm{More}$ and retires the route on a final event. A successful `Cancel` on the issuing loop leaves the route live. [Subscriptions](docs/execution.md#subscriptions)

## Usage

- A nil `next` from `Advance` marks completion. With `AdvanceSuspension` and a `cove.SuspensionView`, check `next.Suspension == nil`. On `iox.ErrWouldBlock`, the dispatcher retains any partial progress. For a guarded write, track writability and peer permission with `tempo.Both`. Check both with `cove.CheckSuspension` before each attempt, including retries, and keep permission valid through resumption. [Guarded write example](write_session_example_test.go)
- Use `Loop` for one-shot requests. If a reply is guarded, the binding must ensure the guard holds when `Poll` delivers it. `Submit` and `SubmitExpr` reserve capacity before evaluation. `SubmitSuspension` transfers ownership only on success. `Poll` offers queued operations to the backend and accounts for the whole batch before delivering eligible replies in observation order.
- Use `SubscriptionLoop` for repeated events from one request. `Subscribe` checks capacity before calling the backend. Success means the route was accepted. `StreamCompletion.More` keeps that `RouteID` live even when the event carries an error or no value.
- Give each loop one owner. Pass it by pointer and serialize all access, including status reads. A callback may read status. Call state-changing methods after it returns.
- Copy the entries you need from `Loop.Poll` before its next `Poll`, `Run`, or `Drain`, and from `SubscriptionLoop.Poll` before its next `Poll` or `Drain`. If an entry refers to borrowed bytes, retain the buffer under the binding's contract or copy the bytes before the borrow ends. Call `Loop.Drain` to release its completion buffer. The backend binding owns outstanding native requests through retirement.
- Construction provisions loop bookkeeping. Measure its first-use and steady-state allocations separately from program and backend costs. [Performance guide](docs/performance.md)

Run `sh verify.sh` from the repository root to check the Go module and the bounded Lean model. [Verification](docs/verification.md) · [Proof scope](proofs/README.md)
