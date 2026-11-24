import Takt.Runner

namespace Takt

/-- Every fetched observation is either accounted or retained for disposal. -/
structure Batch (α : Type) where
  accounted : List α
  remaining : List α

def next (b : Batch α) : Batch α := match b.remaining with
  | [] => b
  | x :: xs => ⟨b.accounted ++ [x], xs⟩

theorem batch_ownership (b : Batch α) :
    (next b).accounted ++ (next b).remaining = b.accounted ++ b.remaining := by
  cases b with
  | mk a r => cases r <;> simp [next, List.append_assoc]

theorem observation_fold (f : σ → α → σ) (s : σ) (xs ys : List α) :
    (xs ++ ys).foldl f s = ys.foldl f (xs.foldl f s) := by
  simp [List.foldl_append]

def offers (budget : Nat) (snapshot : List α) := snapshot.take budget

theorem offer_bound (c : Nat) (q : List α) : (offers c q).length ≤ c := by
  simp only [offers, List.length_take]
  omega

theorem remaining_bound (c n : Nat) : min c n ≤ c := by omega

/-- Accounting selects each visited batch position at most once in order.
    Consequently the same C budget consumes every staged reply before the slab
    can be overwritten or cleared. -/
theorem reply_budget (selected : List (Fin n))
    (hselect : selected.Sublist (List.finRange n)) (hbatch : n ≤ c) :
    min selected.length c = selected.length := by
  have bound := hselect.length_le
  simp only [List.length_finRange] at bound
  omega

/-- Each offer removes the head and appends the entries produced by its outcome. -/
def service (emit : β → α → List α) : List β → List α → List α
  | [], q => q
  | _ :: _, [] => []
  | outcome :: rest, x :: xs => service emit rest (xs ++ emit outcome x)

/-- The untouched suffix is followed by the entries emitted by the offered prefix, in order. -/
theorem prefix_service (emit : β → α → List α) (outcomes : List β) (before after : List α)
    (hlen : outcomes.length = before.length) :
    service emit outcomes (before ++ after) = after ++ (outcomes.zipWith emit before).flatten := by
  induction outcomes generalizing before after with
  | nil =>
    have empty : before = [] := List.length_eq_zero_iff.mp hlen.symm
    subst before
    simp [service]
  | cons outcome rest ih =>
    cases before with
    | nil => simp at hlen
    | cons x xs =>
      have len := Nat.succ.inj hlen
      simpa [service, List.append_assoc] using ih xs (after ++ emit outcome x) len

/-- After an offered prefix, a waiting slot is in the next snapshot exactly when
    fewer older slots remain than the budget. -/
theorem conditional_queue_service (emit : β → α → List α) (outcomes : List β)
    (offered older : List α) (x : α) (after : List α) (budget : Nat)
    (hlen : outcomes.length = offered.length) :
    (offers budget (service emit outcomes (offered ++ (older ++ x :: after))))[older.length]? =
      if older.length < budget then some x else none := by
  rw [prefix_service emit outcomes offered (older ++ x :: after) hlen]
  simp [offers, List.getElem?_take, List.append_assoc]

theorem next_offer_reaches (emit : β → α → List α) (outcomes : List β)
    (before : List α) (x : α) (after : List α) (hlen : outcomes.length = before.length) :
    (service emit outcomes (before ++ x :: after)).head? = some x := by
  rw [prefix_service emit outcomes before (x :: after) hlen]
  rfl

end Takt
