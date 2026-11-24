import Std

namespace Takt

/-- Two exclusive service owners. Residual state includes every future-observable
    obligation; the operation moves that same value without reconstructing it. -/
def handoff (source target : Option α) : Option (Option α × Option α) :=
  match source, target with
  | some residual, none => some (none, some residual)
  | _, _ => none

theorem handoff_preserves_residual (s t s' t' : Option α)
    (h : handoff s t = some (s', t')) :
    ∃ r, s = some r ∧ t = none ∧ s' = none ∧ t' = some r := by
  cases s with
  | none => simp [handoff] at h
  | some r =>
    cases t with
    | some _ => simp [handoff] at h
    | none =>
      have same : (none, some r) = (s', t') := Option.some.inj h
      cases same
      exact ⟨r, rfl, rfl, rfl, rfl⟩

theorem handoff_preserves_observation (observe : α → β) (r : α) :
    (handoff (some r) none).map (fun owners => owners.2.map observe) =
      some (some (observe r)) := by rfl

theorem occupied_target_rejects (s : Option α) (r : α) :
    handoff s (some r) = none := by cases s <;> rfl

end Takt
