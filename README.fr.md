[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | [Español](README.es.md) | [日本語](README.ja.md) | **Français**

# takt

Moteur de dispatch abstrait pilote par evenements de completion pour les piles d'E/S non bloquantes.

## Presentation

Dans un modele proactor, les operations d'E/S sont soumises au noyau et leurs completions arrivent de maniere asynchrone. L'application doit correler chaque completion avec le calcul qui l'a demandee, reprendre ce calcul et gerer toute la gamme de resultats — succes, progres partiel, contrepression et echec.

`takt` fournit cette algebre de dispatch comme une couche abstraite au-dessus du systeme d'effets [kont](https://code.hybscloud.com/kont). Un `Dispatcher` evalue un effet algebrique a la fois, classifiant le resultat selon l'algebre de resultats [iox](https://code.hybscloud.com/iox). Un `Backend` soumet les operations a un moteur asynchrone (ex., `io_uring`) et sonde les completions. La boucle d'evenements `Loop` les relie : elle soumet les calculs, sonde le backend, correle les completions par jeton et reprend les continuations suspendues.

Deux APIs equivalentes : Cont (base sur les fermetures, composition directe) et Expr (defonctionnalise, surcout d'allocation reduit pour les chemins critiques).

## Installation

```bash
go get code.hybscloud.com/takt
```

Requires Go 1.26+.

## Classification des Resultats

Chaque operation dispatchee retourne un resultat `iox`. Le dispatcher et l'API de stepping gerent chaque cas :

| Resultat | Signification | Dispatcher | API de Stepping |
----------|---------------|------------|-----------------|
 `nil` | complete | reprendre | reprendre, retourner `nil` |
 `ErrMore` | progres, suite attendue | reprendre | reprendre, retourner `ErrMore` |
 `ErrWouldBlock` | pas de progres | attendre | retourner la suspension a l'appelant |
 autre | echec d'infrastructure | panic | retourner l'erreur a l'appelant |

## Utilisation

### Dispatcher

Un `Dispatcher` mappe chaque effet algebrique a une operation d'E/S concrete et retourne le resultat avec un resultat `iox`.

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // dispatch op, return (value, nil) or (nil, iox.ErrWouldBlock)
}
```

### Evaluation Bloquante

`Exec` et `ExecExpr` executent un calcul jusqu'a completion, attendant de maniere synchrone quand le dispatcher retourne `iox.ErrWouldBlock`.

```go
result := takt.Exec(d, computation)         // Cont-world
result := takt.ExecExpr(d, exprComputation) // Expr-world
```

### Stepping

Pour les boucles d'evenements proactor (ex., `io_uring`), `Step` et `Advance` evaluent un effet a la fois. Quand le dispatcher retourne `iox.ErrWouldBlock`, la suspension est retournee a l'appelant, permettant a la boucle d'evenements de replanifier.

```go
result, susp := takt.Step[int](exprComputation)
if susp != nil {
    var err error
    result, susp, err = takt.Advance(d, susp)
    if iox.IsWouldBlock(err) {
        return susp // yield to event loop, reschedule when ready
    }
}
// result is the final value
```

### Gestion d'Erreurs

Composez les effets de dispatcher avec les effets d'erreur. `Throw` court-circuite le calcul et abandonne la suspension en attente.

```go
either := takt.ExecError[string](d, computation)
// Right on success, Left on Throw

// Stepping with errors
either, susp := takt.StepError[string, int](exprComputation)
if susp != nil {
    var err error
    either, susp, err = takt.AdvanceError[string](d, susp)
    if iox.IsWouldBlock(err) {
        return susp // yield to event loop, reschedule when ready
    }
}
```

### Boucle d'Evenements

Un `Loop` pilote les calculs a travers un `Backend`. Il soumet les operations, sonde les completions, les correle par `Token` et reprend les continuations suspendues.
`maxCompletions` dans `NewLoop` doit etre strictement superieur a 0.

```go
loop := takt.NewLoop[*myBackend, int](backend, 64)

// Submit computations
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // Cont-world

// Drive all to completion
results, err := loop.Run()
```

## Apercu de l'API

### Dispatch

- `Dispatcher[D Dispatcher[D]]` — F-bounded dispatch interface
- `Exec[D, R](d D, m kont.Eff[R]) R` — blocking Cont-world evaluation
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — blocking Expr-world evaluation

### Stepping

- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — evaluate to first suspension
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — dispatch one operation

### Gestion d'Erreurs

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — blocking with errors
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — Expr-world with errors
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — step with errors
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` — advance with errors

### Backend et Boucle d'Evenements

- `Backend[B Backend[B]]` — F-bounded async submit/poll interface
- `Token` — submission-completion correlation (`uint64`)
- `Completion` — `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, maxCompletions int) *Loop[B, R]` — create event loop (`maxCompletions > 0`)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)` — step and submit Expr
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` — step and submit Cont
- `(*Loop[B, R]).Poll() ([]R, error)` — poll and dispatch completions
- `(*Loop[B, R]).Run() ([]R, error)` — drive all to completion
- `(*Loop[B, R]).Pending() int` — count pending operations

### Pont

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Cont

## References

- G. D. Plotkin and M. Pretnar. "Handlers of Algebraic Effects." In *Proc. ESOP*, 2009.
- T. Uustalu and V. Vene. "Comonadic Notions of Computation." In *ENTCS* 203(5), 2008.
- D. Ahman and A. Bauer. "Runners in Action." In *Proc. ESOP*, 2020.

## Licence

Licence MIT. Voir [LICENSE](LICENSE) pour les details.

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
