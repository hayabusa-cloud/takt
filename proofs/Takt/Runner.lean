import Std

namespace Takt

inductive Phase where
  | free | queued | active | reply
  deriving DecidableEq, Repr

/-- One lineage record. Prefix counts already executed effect replies. -/
structure Record where
  phase : Phase
  executed : Nat
  capture : Nat
  token : Option Nat
  deferred : Bool
  deriving DecidableEq, Repr

/-- Refusal preserves both the capture and the executed prefix. -/
def refuse (r : Record) : Record := { r with deferred := true }

theorem refusal_ownership (r : Record) :
    let next := refuse r
    next.capture = r.capture ∧ next.executed = r.executed ∧
      next.phase = r.phase ∧ next.token = r.token ∧ next.deferred = true :=
  ⟨rfl, rfl, rfl, rfl, rfl⟩

/-- A retired no-reply observation retains the reached suspension. -/
def rearm (r : Record) : Record :=
  { r with phase := .queued, token := none, deferred := true }

theorem rearm_nonreplay (r : Record) :
    let next := rearm r
    next.capture = r.capture ∧ next.executed = r.executed ∧
      next.phase = .queued ∧ next.token = none ∧ next.deferred = true :=
  ⟨rfl, rfl, rfl, rfl, rfl⟩

/-- An affine permission has one terminal disposition. -/
def consume (used : Bool) : Option Bool := if used then none else some true

theorem one_disposition (u v : Bool) (h : consume u = some v) :
    consume v = none := by cases u <;> simp_all [consume]

def finish (r : Record) (successor : Bool) : Record :=
  { phase := if successor then .queued else .free
    executed := r.executed + 1
    capture := r.capture + 1
    token := none
    deferred := false }

/-- Reply premises are re-established or stable at this boundary. -/
def resume (r : Record) (eligible successor : Bool) : Option Record :=
  if r.phase = .reply ∧ eligible = true then some (finish r successor) else none

theorem resume_requires_evidence (r s : Record) (e k : Bool)
    (h : resume r e k = some s) : r.phase = .reply ∧ e = true := by
  unfold resume at h
  split at h
  · assumption
  · contradiction

def Table (n : Nat) := Fin n → Record

def replace (t : Table n) (i : Fin n) (r : Record) : Table n :=
  fun j => if j = i then r else t j

theorem same_slot_successor (t : Table n) (i : Fin n) :
    replace t i (finish (t i) true) i = finish (t i) true ∧
    ∀ j, j ≠ i → replace t i (finish (t i) true) j = t j := by
  constructor
  · simp [replace]
  · intro j h
    simp [replace, h]

def count (p : Phase) (rs : List Record) : Nat :=
  (rs.filter (fun r => r.phase == p)).length

theorem slot_conservation (rs : List Record) :
    count .free rs + count .queued rs + count .active rs + count .reply rs = rs.length := by
  induction rs with
  | nil => simp [count]
  | cons r rs ih =>
    cases h : r.phase <;> simp [count, h] at * <;> omega

/-- Token installation is admitted only for an unoccupied originating identity. -/
def install (live : List Nat) (token : Nat) : Option (List Nat) :=
  if token ∈ live then none else some (token :: live)

theorem install_fresh (live next : List Nat) (token : Nat)
    (h : install live token = some next) : token ∉ live ∧ next = token :: live := by
  unfold install at h
  split at h
  · contradiction
  · simp_all

def ready (failed : Bool) (rs : List Record) : Bool :=
  !failed && rs.any fun r => r.phase == .reply || (r.phase == .queued && !r.deferred)

theorem ready_iff (failed : Bool) (rs : List Record) :
    ready failed rs = true ↔ failed = false ∧
      ∃ r ∈ rs, r.phase = .reply ∨ (r.phase = .queued ∧ r.deferred = false) := by
  simp [ready]

/-- A driver may wait on this loop after arming and rechecking both failure and readiness. -/
def mayPark (armed failed : Bool) (rs : List Record) : Bool :=
  armed && !(failed || ready failed rs)

theorem ready_prevents_parking (rs : List Record) (armed failed : Bool)
    (h : failed = true ∨ ready failed rs = true) : mayPark armed failed rs = false := by
  rcases h with h | h <;> simp [mayPark, h]

theorem deferred_retry_not_ready (r : Record) (h : r.phase = .queued) :
    ready false [refuse r] = false := by simp [ready, refuse, h]

end Takt
