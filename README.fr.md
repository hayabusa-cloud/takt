[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | [Español](README.es.md) | [日本語](README.ja.md) | **Français**

# takt

Moteur de dispatch abstrait, piloté par les événements de complétion, pour les piles d'E/S non bloquantes.

## Présentation

Dans un modèle proactor, les opérations d'E/S sont soumises au noyau et leurs complétions arrivent de manière asynchrone. L'application doit corréler chaque complétion avec le calcul qui l'a demandée, reprendre ce calcul, puis traiter le succès, le progrès avec frontière vivante, la readiness/contre-pression sans progrès et l'échec.

`takt` fournit ce modèle d'exécution comme une couche abstraite au-dessus du système d'effets [kont](https://code.hybscloud.com/kont). Un `Dispatcher` évalue un effet algébrique à la fois et classe le résultat selon l'algèbre de résultats [iox](https://code.hybscloud.com/iox). Un `Backend` soumet les opérations à un moteur asynchrone, par exemple `io_uring`, et sonde les complétions. `Loop` les relie : il soumet les calculs, sonde le backend, corrèle les complétions par jeton et reprend les continuations suspendues.

Deux API équivalentes sont disponibles : `kont.Eff` (à base de fermetures et simple à composer) et `kont.Expr` (à base de frames, avec moins d'allocations sur les chemins critiques).

Le chemin de boucle d'événements garde une seule suspension pendante par jeton vivant. Chaque jeton suit une suspension produite par `kont.StepExpr` (ou après réification de `kont.Eff`), donc `Backend.Submit` ne doit pas réutiliser un jeton tant que la soumission plus ancienne qui le porte reste vivante dans la boucle.

## Limite de composition

`takt` possède le mouvement d'exécution, pas le sens du contexte ni le vocabulaire des résultats. `iox` classe `nil`, `ErrWouldBlock`, `ErrMore` et les échecs ; `kont` possède le porteur de suspension/reprise ; `cove` peut envelopper une suspension avec un contexte explicite via `SuspensionView` ; `takt` avance toute valeur qui satisfait `SuspensionLike` sans interpréter ce contexte.

## Installation

```bash
go get code.hybscloud.com/takt
```

Nécessite Go 1.26+.

## Classification des résultats

Chaque opération dispatchée renvoie un résultat `iox`. `Dispatcher.Dispatch` rapporte ce résultat sous la forme `(value, error)` ; les runners bloquants et l'API de stepping l'interprètent ainsi :

| Résultat        | Signification                 | Retour Dispatcher       | Comportement bloquant/stepping                       |
|-----------------|-------------------------------|-------------------------|------------------------------------------------------|
| `nil`           | terminé                       | `(value, nil)`          | reprend                                              |
| `ErrMore`       | progrès avec frontière vivante | `(value, ErrMore)`      | reprend ; le stepping renvoie `ErrMore`              |
| `ErrWouldBlock` | aucun progrès                 | `(nil, ErrWouldBlock)`  | le bloquant attend ; le stepping renvoie la suspension |
| autre           | échec d'infrastructure        | `(nil, error)`          | le bloquant panique ; le stepping renvoie l'erreur   |

## Utilisation

### Dispatcher

Un `Dispatcher` associe chaque effet algébrique à une opération d'E/S concrète et renvoie le résultat accompagné de son état `iox`.

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	// dispatcher op ; renvoyer (value, nil), (value, iox.ErrMore) ou (nil, iox.ErrWouldBlock)
}
```

### Évaluation bloquante

`Exec` et `ExecExpr` exécutent un calcul jusqu'à complétion en attendant de manière synchrone lorsque le dispatcher renvoie `iox.ErrWouldBlock`.

```go
result := takt.Exec(d, computation) // kont.Eff
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### Pas à pas

Pour les boucles d'événements proactor, par exemple `io_uring`, `Step` et `Advance` évaluent un effet à la fois. Quand le dispatcher renvoie `iox.ErrWouldBlock`, la suspension est rendue à l'appelant, ce qui permet à la boucle d'événements de replanifier.

La loi de résultat du pas manuel est explicite : si `d.Dispatch(susp.Op())` renvoie `nil` ou `iox.ErrMore`, `Advance` reprend la suspension avec la valeur renvoyée et retourne la suspension suivante avec l'erreur originale ; si elle renvoie `iox.ErrWouldBlock` ou un échec ordinaire, `Advance` retourne le résultat zéro, la suspension originale et cette erreur. `ErrMore` est donc un signal de progrès dans le pas manuel, tandis que `ErrMore` au niveau complétion reste non pris en charge par le `Loop` générique parce que l'opération backend reste vivante.

```go
result, susp := takt.Step[int](exprComputation)
if susp != nil {
	var err error
	result, susp, err = takt.Advance(d, susp)
	if iox.IsWouldBlock(err) {
		return susp // céder à la boucle d'événements ; replanifier quand prêt
	}
}
// result contient la valeur finale
```

### Gestion des erreurs

Composez les opérations du dispatcher avec des effets d'erreur. `Throw` court-circuite immédiatement le calcul et abandonne la suspension en attente.

```go
either := takt.ExecError[string](d, computation)
// Right en cas de succès, Left en cas de Throw

// Pas à pas avec erreurs
either, susp := takt.StepError[string, int](exprComputation)
if susp != nil {
	var err error
	either, susp, err = takt.AdvanceError[string](d, susp)
	if iox.IsWouldBlock(err) {
		return susp // céder à la boucle d'événements ; replanifier quand prêt
	}
}
```

### Boucle d'événements

Un `Loop` pilote les calculs au travers d'un `Backend`. Il soumet les opérations, sonde les complétions, les corrèle par `Token` et reprend les continuations suspendues. `NewLoop` accepte des `Option` fonctionnelles. `WithMaxCompletions(n)` panique si `n <= 0` avec `takt: WithMaxCompletions requires n > 0` ; `WithMemory(nil)` panique avec `takt: WithMemory requires a non-nil CompletionMemory`.

`Backend.Poll([]Completion) (int, error)` rapporte à la fois le nombre de complétions prêtes et tout échec d'infrastructure de sondage. Le `Loop` traite un `iox.ErrWouldBlock` renvoyé par `Poll` comme un tour à vide plutôt que comme une erreur terminale.

`Loop` est un runner à propriétaire unique. Sérialisez les appels qui partagent la même `Loop`, notamment `SubmitExpr`, `Submit`, `Poll`, `Run`, `Drain`, `Pending` et `Failed`.

`Backend.Submit` doit renvoyer un jeton unique parmi toutes les soumissions encore vivantes dans la boucle. Si un backend réutilise un jeton vivant, la boucle enregistre `ErrLiveTokenReuse`, écarte chaque suspension pendante exactement une fois, puis tout appel ultérieur à `SubmitExpr` / `Submit` / `Poll` / `Run` renvoie cette erreur fatale.

Lorsqu'une complétion porte `iox.ErrWouldBlock`, la boucle resoumet la même opération. Si une complétion porte `iox.ErrMore` (multishot), la boucle enregistre `ErrUnsupportedMultishot`, écarte chaque suspension pendante exactement une fois, et tout appel ultérieur à `SubmitExpr` / `Submit` / `Poll` / `Run` renvoie cette erreur fatale. `ErrMore` signifie que l'opération backend soumise reste active après la CQE, tandis que le `Loop` générique n'a pas de porteur d'abonnement ou d'annulation pour les complétions ultérieures du même jeton.

`Loop.Failed()` renvoie l'erreur fatale enregistrée (ou `nil`). `Loop.Drain()` force la boucle dans l'état disposé, écarte chaque suspension pendante exactement une fois et n'enregistre `ErrDisposed` que si aucune erreur fatale n'était déjà présente ; la méthode est idempotente et préserve toute erreur fatale préexistante.

```go
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// Soumettre des calculs
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // kont.Eff

// Faire avancer le tout jusqu'à complétion
results, err := loop.Run()
```

`NewLoop` utilise [`HeapMemory`](completion_memory.go) comme fournisseur par défaut du buffer de complétion. Si vous voulez que ces buffers proviennent d'un pool borné et stable de slabs de 128 KiB de taille par défaut, passez [`BoundedMemory`](completion_memory.go) via [`WithMemory`](option.go) ; ou fournissez n'importe quelle implémentation de `CompletionMemory` pour contrôler la stratégie d'allocation sans élargir les contrats de `Backend` ni de `Completion`. Les fournisseurs personnalisés doivent renvoyer des slabs vivants exclusifs et non chevauchants, et peuvent traiter `Release` comme un transfert de propriété vers le fournisseur :

```go
// Par défaut : HeapMemory + slab de complétion de taille par défaut.
loop := takt.NewLoop[*myBackend, int](backend)

// Limite la longueur du slab de complétion par scrutation (Memory choisit toujours la forme du slab ; Loop tronque la longueur visible à ce plafond).
loop = takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// Partage le sync.Pool d'un même HeapMemory entre plusieurs Loops en passant la même adresse à WithMemory ; copier une valeur HeapMemory ne partagerait pas les slabs recyclés.
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))
loopB := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))

// BoundedMemory : un pool borné de slabs de 128 KiB de taille par défaut. WithPoolCapacity ajuste la capacité de ce pool (iobuf l'arrondit à la puissance de deux supérieure).
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
loop = takt.NewLoop[*myBackend, int](
	backend,
	takt.WithMemory(bounded),
	takt.WithMaxCompletions(64),
)
```

## Aperçu de l'API

### Dispatch

- `Dispatcher[D Dispatcher[D]]`: interface de dispatch non bloquante
- `Exec[D, R](d D, m kont.Eff[R]) R`: évaluation bloquante de `kont.Eff`
- `ExecExpr[D, R](d D, m kont.Expr[R]) R`: évaluation bloquante de `kont.Expr`

### Pas à pas

- `SuspensionLike[S, R]`: interface reprenable avec `Op` et `Resume`, implémentée par `cove.SuspensionView`
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])`: évalue jusqu'à la première suspension
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)`: dispatche une opération via toute valeur `SuspensionLike`
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)`: dispatche une opération

### Gestion des erreurs

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]`: bloquante avec erreurs
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]`: évaluation de `kont.Expr` avec gestion d'erreurs
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])`: pas à pas avec erreurs
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)`: avancée avec erreurs

### Backend et boucle d'événements

- `Backend[B Backend[B]]`: interface asynchrone de submit/poll
- `CompletionMemory`: fournisseur local du buffer de complétion de la boucle
- `HeapMemory`: implémentation par défaut (`sync.Pool` avec slabs typés de taille par défaut)
- `BoundedMemory`: implémentation adossée à iobuf avec un unique pool borné de slabs de 128 KiB de taille par défaut
- `Option`: option fonctionnelle pour `NewLoop` (`WithMemory`, `WithMaxCompletions`)
- `CompletionBufOption`: option fonctionnelle pour `CompletionMemory.CompletionBuf` (`WithSize`)
- `BoundedMemoryOption`: option fonctionnelle pour `NewBoundedMemory` (`WithPoolCapacity`)
- `Token`: corrélation soumission-complétion (`uint64`)
- `Completion`: `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, opts ...Option) *Loop[B, R]`: crée une boucle d'événements (avec `HeapMemory` et un slab par défaut si rien n'est précisé)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)`: avance d'un pas et soumet un Expr
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)`: avance d'un pas et soumet un `kont.Eff`
- `(*Loop[B, R]).Poll() ([]R, error)`: sonde et dispatche les complétions
- `(*Loop[B, R]).Run() ([]R, error)`: fait avancer le tout jusqu'à complétion
- `(*Loop[B, R]).Pending() int`: nombre d'opérations en attente
- `(*Loop[B, R]).Failed() error`: erreur fatale terminale, ou nil
- `(*Loop[B, R]).Drain() int`: abandonne les suspensions en attente et dispose la boucle
- `ErrLiveTokenReuse`: le backend a réutilisé un jeton encore vivant dans la boucle
- `ErrUnsupportedMultishot`: une complétion multishot n'est pas prise en charge par le `Loop` générique
- `ErrDisposed`: la boucle a été disposée via `Drain`

### Pont

- `Reify[A](kont.Eff[A]) kont.Expr[A]`: Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]`: Expr → Cont

## Schémas pratiques

Une intégration complète à une boucle d'événements combine un `Dispatcher` (la sémantique synchrone) avec un `Backend` (le proactor) sous une même `Loop` :

```go
// 1. Définir le dispatcher : associe une opération d'effet à un résultat iox.
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	// Renvoyer (value, nil), (value, iox.ErrMore) ou (nil, iox.ErrWouldBlock).
}

// 2. Définir le backend : soumet les opérations au proactor de l'OS et sonde les complétions.
type myBackend struct{ /* ... */ }
func (b *myBackend) Submit(op kont.Operation) (takt.Token, error) { /* ... */ }
func (b *myBackend) Poll(out []takt.Completion) (int, error)      { /* ... */ }

// 3. Piloter : soumettre un ou plusieurs calculs, puis Run jusqu'à complétion.
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))
loop.SubmitExpr(prog1)
loop.SubmitExpr(prog2)
results, err := loop.Run()
_ = results; _ = err
```

Pour la composition avec gestion d'erreurs, utilisez `ExecError` / `StepError` / `AdvanceError` à la place de leurs équivalents sans erreur ; `Throw` court-circuite le calcul en cours sans affecter les calculs frères de la même boucle. Le motif fusionné dispatcher+backend illustré ici est précisément celui qu'utilise `sess` pour rattacher un endpoint de session à un runtime d'E/S réel.

## Références

- Tarmo Uustalu and Varmo Vene. 2008. Comonadic Notions of Computation. *Electronic Notes in Theoretical Computer Science* 203, 5 (June 2008), 263–284. https://doi.org/10.1016/j.entcs.2008.05.029
- Gordon D. Plotkin and Matija Pretnar. 2009. Handlers of Algebraic Effects. In *Proc. 18th European Symposium on Programming (ESOP '09)*. LNCS 5502, 80–94. https://doi.org/10.1007/978-3-642-00590-9_7
- Andrej Bauer and Matija Pretnar. 2015. Programming with Algebraic Effects and Handlers. *Journal of Logical and Algebraic Methods in Programming* 84, 1 (Jan. 2015), 108–123. https://arxiv.org/abs/1203.1539
- Daniel Leijen. 2017. Type Directed Compilation of Row-Typed Algebraic Effects. In *Proc. 44th ACM SIGPLAN Symposium on Principles of Programming Languages (POPL '17)*. 486–499. https://doi.org/10.1145/3009837.3009872
- Danel Ahman and Andrej Bauer. 2020. Runners in Action. In *Proc. 29th European Symposium on Programming (ESOP '20)*. LNCS 12075, 29–55. https://arxiv.org/abs/1910.11629
- Daniel Hillerström, Sam Lindley, and Robert Atkey. 2020. Effect Handlers via Generalised Continuations. *Journal of Functional Programming* 30 (2020), e5. https://bentnib.org/handlers-cps-journal.pdf

## Licence

Licence MIT. Voir [LICENSE](LICENSE) pour les détails.

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
