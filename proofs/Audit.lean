import Takt
import Lean

open Lean Elab Command in
run_cmd do
  let environment ← getEnv
  let permitted := #[`propext, `Quot.sound]
  let required := #[`Takt.refusal_ownership, `Takt.rearm_nonreplay,
    `Takt.one_disposition, `Takt.resume_requires_evidence, `Takt.slot_conservation,
    `Takt.same_slot_successor, `Takt.install_fresh, `Takt.ready_iff,
    `Takt.ready_prevents_parking, `Takt.deferred_retry_not_ready,
    `Takt.batch_ownership, `Takt.observation_fold, `Takt.offer_bound,
    `Takt.conditional_queue_service, `Takt.prefix_service,
    `Takt.next_offer_reaches, `Takt.handoff_preserves_residual,
    `Takt.handoff_preserves_observation, `Takt.occupied_target_rejects,
    `Takt.Ring.offset_injective, `Takt.Ring.push_refines, `Takt.Ring.pop_refines,
    `Takt.Ring.step_valid, `Takt.Ring.run_refines, `Takt.Ring.round_refines,
    `Takt.Ring.fifo_service, `Takt.Ring.push_wrap, `Takt.Ring.pop_wrap, `Takt.reply_budget]
  let mut theorems : NameSet := {}
  for (name, info) in environment.constants.toList do
    if (environment.getModuleIdxFor? name).any
        (fun i => (`Takt).isPrefixOf environment.header.moduleNames[i]!) then
      if let .axiomInfo _ := info then throwError "Local axiom: {name}"
      if info.isTheorem then theorems := theorems.insert name
  if theorems.isEmpty then throwError "No model theorems audited"
  for name in required do
    unless (environment.find? name).any ConstantInfo.isTheorem do
      throwError "Missing required theorem: {name}"
    theorems := theorems.insert name
  for name in theorems do
    for dependency in ← collectAxioms name do
      unless permitted.contains dependency do
        throwError "Forbidden axiom {dependency} in {name}"
  logInfo m!"Audited {theorems.size} theorems; allowed axioms: {permitted}"
