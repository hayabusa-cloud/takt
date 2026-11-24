# Performance

Runner construction provisions records, queues and observation storage. Each
loop has one serialized owner. Its identity index has 1,024 bucket chains
sharing one record arena. Scalar lookups hash the token. Subscription lookups
rotate the generation by 32 bits before combining it with the token, so
distinct routes with `token == generation` can reach different buckets.
Lookup costs O(capacity) in the worst case.

Pending records retain a reply's batch index instead of copying its completion.
All eligible replies are consumed before the slab is cleared or polled again.
Circular queues wrap at their configured capacity, which can be any positive
integer. Compact records reduce storage and copying. Measure cache locality
under the intended workload, especially when moving ownership between cores.

`Loop.Poll` runs on the caller's goroutine and budgets offers, observations
and resumptions separately. `Loop.Run` uses `iox.Backoff`. Fresh work keeps it
running, and resumption resets the backoff. External drivers combine local
readiness with provider wakes, cleanup and deadlines. Elapsed time also
depends on how long the backend and user callbacks take.

`Dispatcher`, `Backend`, `SubscriptionBackend` and `SuspensionLike` retain typed
generic families. F-bounds do not guarantee that a compiler removes every
dictionary or indirect call. Inspect instantiated code before claiming direct
dispatch or cost equivalence. Completion-slab providers remain cold interfaces.

## Workloads

| Benchmark | Timed work |
|---|---|
| `LoopRefusal` | Bounded FIFO retry rounds over already admitted captures, varying capacity and offer budget. Excludes sleeping. |
| `LoopBatch` | Submit the same expression repeatedly, capturing a fresh suspension each time, then poll once for the batch in reverse order. |
| `SubscriptionLocality` | Poll 64 live events per iteration, cycling through the admitted routes. |
| `SubscriptionRetirement` | Route admission through terminal observation and slot reuse, in batches of at most 64 events. The closed provider has no late observations. |
| `DispatchFamily` | Direct, typed structural stepping and an erased dispatcher adapter for the same scalar protocol. |
| `DispatchControl` | Structural stepping across nil, semantic sentinels, ordinary failure, wrapped errors and overlapping sentinels. Error construction is outside timing. |
| `GuardedWriteRetry` | One writable wake for a pending write against a full in-memory send buffer. Includes the route match, `tempo.Both` updates, `cove` check and refused dispatch. |
| `AdvanceErrorRefusal` | Retry one retained capture on WouldBlock or failure. Construction and disposal occur outside timing. |

The batch and subscription workloads cover 1, 64, 1,024, 2,048, 8,192 and 32,768
identities with token strides of 1 and 64. Both subscription workloads also
exercise 1,024 routes whose token equals their generation. Retirement times
admission of every route and as many polls as it takes to retire them all.

`LoopBatch` includes `kont` capture construction. Use the first-use allocation
tests to isolate provisioned runner bookkeeping. Measure programs, continuation
creation, decoders and providers separately. `Loop.Run` allocates a caller-owned
result slice when it has pending work.

## Measurement

Run from the module directory. Use the same compiler, flags, kernel, CPU and
affinity for both revisions. Disable workspace overrides so each checkout
selects its declared dependencies. Capture ten samples on each revision.

```sh
export GOWORK=off GOMAXPROCS=1
go test -mod=readonly -run '^$' \
  -bench '^Benchmark(LoopRefusal|LoopBatch|SubscriptionLocality|SubscriptionRetirement|DispatchFamily|DispatchControl|GuardedWriteRetry|AdvanceErrorRefusal)$' \
  -benchmem -benchtime=200ms -count=10 -timeout=5m . > baseline.txt
# Repeat after the change, writing current.txt.
benchstat baseline.txt current.txt
```

Use `benchstat` to compare the two files. Interleave runs when clock frequency
or temperature drifts. The [Benchmarks workflow](../.github/workflows/benchmarks.yml)
runs the same workloads. Its artifact contains all samples and the runner environment.

`ns/op` is the mean time for one benchmark iteration. Use `ns/offer` for
refused submissions, `ns/result` for scalar batches, `ns/event` for live stream
polling, and `ns/route` for admission through retirement. To measure tail
latency, record individual observations including queueing time at a stated
offered load.

Inspect the compiled dispatch path and profile one batch size and stride at a time.

```sh
export GOWORK=off GOMAXPROCS=1
go test -mod=readonly -c -gcflags='code.hybscloud.com/takt...=-m=2' 2> escape.txt
go tool objdump -s 'code.hybscloud.com/takt.*AdvanceSuspension' takt.test
go test -mod=readonly -run '^$' -bench '^BenchmarkLoopBatch$/^count=1024$/^stride=64$' \
  -benchtime=200ms -timeout=120s -cpuprofile=cpu.out -memprofile=mem.out .
go tool pprof -top takt.test cpu.out
go tool pprof -alloc_space -top takt.test mem.out
```

Check generated calls, retained roots and first-use behavior alongside sampled
costs. The compiler pattern includes the external test package where the
generic dispatchers are instantiated. For native comparisons, match the
ownership, cancellation and scheduling conditions in the [execution contract](execution.md).
