# Verification

Run `sh verify.sh` from the checkout with Go 1.27+ and the Lean toolchain named
in `proofs/lean-toolchain` installed through Elan. On Linux, the Go race detector
requires cgo and a C compiler. `TAKT_GO=/path/to/go` selects an installed Go
compiler. The script checks formatting and runs `go mod tidy -diff`, which
exits with a diff if `go.mod` or `go.sum` needs an update. The remaining
checks run vet and race-enabled tests with coverage, build for four Linux
architectures and audit the proofs. The script uses the declared module graph
with workspace overrides disabled and installs no tools.
Set `GOPROXY=off` to require cached modules. CI runs Go checks and pinned proofs.

`TestLoopHistories` checks one-shot completions and `TestStreamHistories` checks
multishot subscriptions. Both compare deterministic in-memory histories with
list/map oracles. Cases cover bounded admission, retries, completion reordering,
collision chains, slot reuse, stale route generations, successor captures,
fatal prefixes and cancellation.

Dispatcher regression tests cover wrapped and joined semantic errors across
plain, contextual and error-aware stepping and the blocking runners. They check
WouldBlock precedence, preserved suspension/context/error identity, and delivery
of the eventual valid reply. A wrapped More alone still permits one resumption.

Additional bounded counterexample search uses Go's standard fuzz runner:

```sh
export GOWORK=off
go test -timeout=120s -run '^$' -fuzz '^FuzzLoopHistory$' -fuzztime=30s
go test -timeout=120s -run '^$' -fuzz '^FuzzStreamHistory$' -fuzztime=30s
```

The [proof model](../proofs/README.md) connects modular circular storage to list
FIFO operations. It checks the queue order after a bounded offer phase. A later
poll can accept a slot that an earlier poll deferred. `sh proofs/run.sh` checks
the documented theorems and their axiom dependencies. The mapping from this
model to the Go queue is reviewed by hand. The full driver, compiler, native
resource ownership, scheduler and physical deadlines are outside the proof.

Allocation tests measure provisioned runner bookkeeping on fresh and reused
instances. Program construction, arbitrary continuations/decoders/backends and
`Run` result collection have separate costs. Native bindings need their own
resource-lifetime tests on a real kernel under the
[execution contract](execution.md).

The [performance guide](performance.md) provides matched workloads, allocation
boundaries and compiler inspection commands.
