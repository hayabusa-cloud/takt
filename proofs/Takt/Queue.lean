import Takt.Trace

namespace Takt.Ring

/-- Fixed storage with a head index and an occupied count. Cells outside the FIFO are unspecified. -/
structure Queue (α : Type) (capacity : Nat) where
  cells : Fin capacity → α
  head : Fin capacity
  size : Nat

def Valid (q : Queue α n) : Prop := q.size ≤ n

def read (q : Queue α n) (i : Nat) : α :=
  q.cells ⟨(q.head.val + i) % n, Nat.mod_lt _ q.head.pos⟩

def contents (q : Queue α n) : List α :=
  List.ofFn fun i : Fin q.size => read q i.val

def wrap (offset capacity : Nat) : Nat :=
  if capacity ≤ offset then offset - capacity else offset

def push (q : Queue α n) (x : α) : Queue α n :=
  { q with cells := fun i => if i.val = wrap (q.head.val + q.size) n then x else q.cells i
           size := q.size + 1 }

/-- Return the slot at the old head and reset the incremented head when it reaches capacity. -/
def pop (q : Queue α n) : α × Queue α n :=
  let next := q.head.val + 1
  let nextHead : Fin n := ⟨if next = n then 0 else next, by
    have := q.head.isLt
    dsimp [next]
    split <;> omega⟩
  (q.cells q.head, { q with head := nextHead, size := q.size - 1 })

theorem wrap_eq_mod (hx : x < 2 * n) : wrap x n = x % n := by
  unfold wrap
  split
  · rw [Nat.mod_eq_sub_mod (by omega)]
    exact (Nat.mod_eq_of_lt (by omega)).symm
  · exact (Nat.mod_eq_of_lt (by omega)).symm

theorem push_wrap (q : Queue α n) (hs : q.size < n) :
    wrap (q.head.val + q.size) n = (q.head.val + q.size) % n := by
  have := q.head.isLt
  exact wrap_eq_mod (by omega)

theorem pop_wrap (q : Queue α n) : (pop q).2.head.val = (q.head.val + 1) % n := by
  change (if q.head.val + 1 = n then 0 else q.head.val + 1) = _
  split
  · rename_i h
    simp [h]
  · have := q.head.isLt
    exact (Nat.mod_eq_of_lt (by omega)).symm

/-- Distinct occupied offsets never alias, including across wraparound. -/
theorem offset_injective {h i j n : Nat} (hh : h < n) (hi : i < n) (hj : j < n)
    (he : (h + i) % n = (h + j) % n) : i = j := by
  simp only [Nat.add_mod_eq_sub, Nat.mod_eq_of_lt hh,
    Nat.mod_eq_of_lt hi, Nat.mod_eq_of_lt hj] at he
  split at he <;> split at he <;> omega

theorem push_valid (q : Queue α n) (x : α) (hs : q.size < n) :
    Valid (push q x) := Nat.succ_le_of_lt hs

theorem pop_valid (q : Queue α n) (hv : Valid q) : Valid (pop q).2 :=
  Nat.le_trans (Nat.sub_le q.size 1) hv

theorem push_preserves (q : Queue α n) (x : α) (hs : q.size < n) (hi : i < q.size) :
    read (push q x) i = read q i := by
  have ne : (q.head.val + i) % n ≠ (q.head.val + q.size) % n := by
    intro he
    have := offset_injective q.head.isLt (Nat.lt_trans hi hs) hs he
    omega
  simp [read, push, push_wrap q hs, ne]

theorem push_last (q : Queue α n) (x : α) (hs : q.size < n) :
    read (push q x) q.size = x := by simp [read, push, push_wrap q hs]

theorem push_refines (q : Queue α n) (x : α) (hs : q.size < n) :
    contents (push q x) = contents q ++ [x] := by
  change (List.ofFn fun i : Fin (q.size + 1) => read (push q x) i.val) = _
  rw [List.ofFn_succ_last]
  have same : (fun i : Fin q.size => read (push q x) i.castSucc.val) =
      (fun i : Fin q.size => read q i.val) := by
    funext i
    exact push_preserves q x hs i.isLt
  rw [same]
  simp [contents, push_last q x hs]

theorem pop_read (q : Queue α n) (i : Nat) :
    read (pop q).2 i = read q (i + 1) := by
  change q.cells ⟨((pop q).2.head.val + i) % n, _⟩ = q.cells ⟨(q.head.val + (i + 1)) % n, _⟩
  congr 1
  apply Fin.ext
  change ((pop q).2.head.val + i) % n = (q.head.val + (i + 1)) % n
  rw [pop_wrap, Nat.mod_add_mod, Nat.add_assoc, Nat.add_comm 1 i]

theorem pop_refines (q : Queue α n) (hs : 0 < q.size) :
    contents q = (pop q).1 :: contents (pop q).2 := by
  rcases q with ⟨cells, head, size⟩
  cases size with
  | zero => simp at hs
  | succ k =>
    change (List.ofFn fun i : Fin (k + 1) => read ⟨cells, head, k + 1⟩ i.val) = _
    rw [List.ofFn_succ]
    congr 1
    · simp [read, pop, Nat.mod_eq_of_lt head.isLt]
    · apply congrArg List.ofFn
      funext i
      exact (pop_read ⟨cells, head, k + 1⟩ i.val).symm

/-- One queue action with its admission preconditions. -/
inductive Step : Queue α n → Queue α n → Prop where
  | push (q : Queue α n) (x : α) (hs : q.size < n) : Step q (push q x)
  | pop (q : Queue α n) (hv : Valid q) (hs : 0 < q.size) : Step q (pop q).2

theorem step_valid (h : Step q q') : Valid q' := by
  cases h with
  | push x hs => exact push_valid _ x hs
  | pop hv _ => exact pop_valid _ hv

/-- A refused offer appends the slot returned by pop. -/
def offer (q : Queue α n) (refused : Bool) : Queue α n :=
  let (slot, rest) := pop q
  if refused then push rest slot else rest

private theorem room_after_pop (q : Queue α n) (hv : Valid q) (hs : 0 < q.size) :
    (pop q).2.size < n :=
  Nat.lt_of_lt_of_le (Nat.sub_lt hs Nat.zero_lt_one) hv

theorem offer_valid (q : Queue α n) (refused : Bool)
    (hv : Valid q) (hs : 0 < q.size) : Valid (offer q refused) := by
  cases refused with
  | false => exact pop_valid q hv
  | true => exact push_valid _ _ (room_after_pop q hv hs)

/-- Entries appended to the list after one offer. -/
def requeued (refused : Bool) (x : α) : List α := if refused then [x] else []

theorem offer_refines (q : Queue α n) (refused : Bool) (hv : Valid q) (hs : 0 < q.size) :
    contents (offer q refused) = contents (pop q).2 ++ requeued refused (pop q).1 := by
  cases refused with
  | false => simp [offer, requeued]
  | true => exact push_refines _ _ (room_after_pop q hv hs)

/-- One flag per offer, in order. A true flag records a refusal. -/
def run : List Bool → Queue α n → Queue α n
  | [], q => q
  | refused :: rest, q => if q.size = 0 then q else run rest (offer q refused)

/-- The ring and list process the same chronological sequence of offer outcomes. -/
theorem run_refines (q : Queue α n) (hv : Valid q) (refusals : List Bool) :
    contents (run refusals q) = Takt.service requeued refusals (contents q) := by
  induction refusals generalizing q with
  | nil => rfl
  | cons refused rest ih =>
    by_cases hz : q.size = 0
    · have empty : contents q = [] := by simp [contents, hz]
      simp [run, hz, empty, Takt.service]
    · have hs : 0 < q.size := by omega
      rw [run, ite_eq_right hz, ih _ (offer_valid q refused hv hs), offer_refines q refused hv hs]
      rw [pop_refines q hs]
      rfl

/-- One complete offer phase leaves unoffered slots first, then refused slots in offer order. -/
theorem round_refines (q : Queue α n) (hv : Valid q) (budget : Nat)
    (refusals : List Bool) (hlen : refusals.length = min budget q.size) :
    contents (run refusals q) = (contents q).drop budget ++
      (refusals.zipWith requeued ((contents q).take budget)).flatten := by
  have len : refusals.length = ((contents q).take budget).length := by
    simpa [contents] using hlen
  rw [run_refines q hv]
  simpa using Takt.prefix_service requeued refusals
    ((contents q).take budget) ((contents q).drop budget) len

/-- After one offer per older slot, the waiting slot is at the head. -/
theorem fifo_service (q : Queue α n) (hv : Valid q) (refusals : List Bool)
    (before after : List α) (x : α) (h : contents q = before ++ x :: after)
    (hlen : refusals.length = before.length) : (contents (run refusals q)).head? = some x := by
  rw [run_refines q hv, h]
  exact Takt.next_offer_reaches _ refusals before x after hlen

end Takt.Ring
