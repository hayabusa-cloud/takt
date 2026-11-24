# Bounded runner proofs

From the repository root, run `sh proofs/run.sh` with the pinned Lean toolchain
installed through Elan. The model uses only Lean and Std. The audit checks
every theorem in the `Takt` modules and every theorem named in the table. It allows
only `propext` and `Quot.sound` as transitive axioms.

| Model statement | Implementation boundary |
|---|---|
| `refusal_ownership`, `rearm_nonreplay` | A refused offer stays queued. A retired no-reply completion removes the old token and requeues the suspension. Both defer the retry and preserve the executed prefix. |
| `one_disposition` | Affine use permission; actual capture consumption is supplied by `kont`. |
| `resume_requires_evidence` | Replies require current or stable premises. Raw Loop admits stable replies only. |
| `same_slot_successor`, `slot_conservation` | `table` records are reserved once per lineage and reused by successors. |
| `install_fresh` | Duplicate live identity is rejected before index insertion. Native origin lifetime remains a binding premise. |
| `ready_iff`, `deferred_retry_not_ready` | A healthy loop is ready exactly when it has fresh queued work or a retained reply. A deferred retry alone leaves `Loop.Ready` false. |
| `ready_prevents_parking` | A driver waiting on this loop rechecks for ready work or a stored failure. Either rules out parking. |
| `batch_ownership`, `observation_fold` | Fixed observation words retain an accounted prefix and an owned suffix. |
| `offer_bound`, `conditional_queue_service` | After a prefix has been offered, a waiting slot with `k` older slots ahead is at position `k` in the next offer snapshot exactly when `k < budget`. |
| `prefix_service`, `next_offer_reaches` | Entries appended during an offer stay behind the untouched suffix. After one offer per older slot, the waiting slot reaches the head. |
| `Ring.offset_injective`, `push_refines`, `pop_refines` | Push appends a slot and preserves every occupied entry. Pop returns the old head slot and leaves the FIFO tail. |
| `Ring.step_valid`, `run_refines`, `fifo_service` | The ring preserves its bounds and agrees with list FIFO service for any finite sequence of acceptances and refusals. |
| `Ring.round_refines` | A complete offer phase leaves unoffered slots first, followed by refused slots in their original order. |
| `Ring.push_wrap`, `pop_wrap` | Push subtracts capacity when the tail index reaches or exceeds it. Pop resets the head to zero when the increment reaches capacity. Both agree with modular addressing. |
| `reply_budget` | A sublist of a batch contains at most its initialized entries; the chosen resumption budget consumes every selected reply. |
| `handoff_preserves_residual`, `handoff_preserves_observation` | The model transfers the same residual state from one owner to an empty target. |

`Ring.Queue` uses `Fin capacity` for the head and storage indices and tracks
the occupied count separately. The mapping assumes Go slice assignment,
exclusive access and overflow-free arithmetic. The driver must maintain the
disjoint slot partition that justifies each push and pop. [Go queue](../table.go)

Each offer has its own outcome, so the same slot can be refused and later
accepted. A complete offer phase in `Loop.Poll` makes
`min(initial queue size, batch budget)` attempts. The round theorem describes
the queue before completion processing and assumes the phase finishes without
a fatal error. Future polls supply more attempts. The backend decides which succeed.

Native resource disposal, syscall behavior, physical deadlines and cost
remain separate obligations. Changing batch size changes schedules, so the fold
law alone does not justify whole-program equivalence. Arbitrary callback panics,
contextual refresh and resource-producing abort are outside the raw profile.

See [verification](../docs/verification.md) for behavioral histories and CI.
