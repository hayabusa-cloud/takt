[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Benchmarks](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml/badge.svg)](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | [Español](README.es.md) | [日本語](README.ja.md) | **Français**

# takt

Moteur de dispatch abstrait, piloté par les événements de complétion, pour les piles d'E/S non bloquantes.

## Présentation

Dans le modèle proactor, un backend accepte les requêtes d'E/S et en signale la complétion de façon asynchrone. Lorsqu'une complétion correspond à une requête en cours et fournit une réponse valide, par exemple le nombre d'octets écrits, la boucle reprend le calcul en attente.

`takt` fournit ce modèle d'exécution comme une couche abstraite au-dessus du système d'effets [kont](https://code.hybscloud.com/kont). Un `Dispatcher` évalue un effet algébrique à la fois. `Advance` interprète le résultat selon l'algèbre de résultats [iox](https://code.hybscloud.com/iox). Un `Backend` soumet les opérations à un moteur asynchrone, par exemple `io_uring`, et sonde les complétions. `Loop` soumet les opérations au backend, le sonde, corrèle les complétions par jeton et reprend les continuations suspendues.

Les programmes ont deux représentations : `kont.Eff` utilise des fermetures pour la composition, et `kont.Expr` expose des frames pour avancer par étapes. `Reify` et `Reflect` les relient. Les coûts d'allocation et d'exécution dépendent du programme et de sa réalisation ; aucune représentation ne garantit à elle seule un chemin critique moins coûteux.

Le chemin de boucle d'événements garde une seule suspension pendante par jeton vivant. Chaque jeton suit une suspension produite par `kont.StepExpr` (ou après réification de `kont.Eff`), donc `Backend.Submit` ne doit pas réutiliser un jeton tant que la soumission plus ancienne qui le porte reste vivante dans la boucle.

Pour les intégrations de type stream ou multishot, `SubscriptionLoop` fournit un exécuteur abstrait de routes séparé. Il suit les valeurs `Subscription` par `RouteID` (`Token` plus génération), sonde les valeurs `StreamCompletion`, émet des observations `StreamEvent` et conserve `More` comme preuve de vie de route indépendante de la valeur de charge utile ou de l'erreur de charge utile. Le `Loop` générique reste ainsi affine et one-shot, tandis que les environnements d'exécution concrets disposent d'un endroit explicite pour représenter les observations successeures de la même opération.

## Limite de composition

`takt` possède le mouvement d'exécution, pas le sens du contexte ni le vocabulaire des résultats. `iox` classe `nil`, `ErrWouldBlock`, `ErrMore` et les échecs ; `kont` possède le porteur de suspension/reprise ; `cove` peut envelopper une suspension avec un contexte explicite via `SuspensionView` ; `takt` avance toute valeur qui satisfait `SuspensionLike` sans interpréter ce contexte.

## Installation

```bash
go get code.hybscloud.com/takt
```

Nécessite Go 1.27+.

## Classification des résultats

Chaque opération dispatchée renvoie un résultat `iox`. `Dispatcher.Dispatch` rapporte ce résultat sous la forme `(value, error)` ; les exécuteurs bloquants et l'API de stepping l'interprètent ainsi :

| Résultat        | Signification                 | Retour Dispatcher       | Comportement bloquant/stepping                       |
|-----------------|-------------------------------|-------------------------|------------------------------------------------------|
| `nil`           | réponse valide | `(value, nil)`          | reprend                                              |
| `ErrMore`       | réponse valide ; interaction active | `(value, ErrMore)`      | reprend ; le stepping renvoie `ErrMore`              |
| `ErrWouldBlock` | pas de réponse ; réessayer plus tard | `(nil, ErrWouldBlock)`  | le bloquant attend ; le stepping renvoie la suspension |
| autre           | échec d'infrastructure        | `(nil, error)`          | le bloquant panique ; le stepping renvoie l'erreur   |

Le tableau décrit le contrat de réponse de Dispatcher. Le contrôle `iox` ne mesure pas les octets transférés et ne prouve pas la libération des ressources ; le dispatcher conserve le travail partiel lorsqu'il renvoie WouldBlock.

La répartition suit `iox.Classify`, y compris pour les erreurs enveloppées. Une erreur correspondant à la fois à `ErrWouldBlock` et à `ErrMore` est classée WouldBlock : la suspension est conservée pour une nouvelle tentative ultérieure.

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
```

```go
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### Pas à pas

`Step` évalue une expression jusqu'à ce qu'elle renvoie une valeur ou se suspende en attente de la réponse à une opération. `Advance` transmet directement cette opération au dispatcher. Si le dispatcher écrit des octets avant de renvoyer `iox.ErrWouldBlock`, il conserve la position d'écriture. L'appelant garde la même suspension pour réessayer.

La loi de résultat du pas manuel est explicite : si `d.Dispatch(susp.Op())` renvoie `nil` ou `iox.ErrMore`, `Advance` reprend la suspension avec la valeur renvoyée et retourne la suspension suivante avec l'erreur originale ; si elle renvoie `iox.ErrWouldBlock` ou un échec ordinaire, `Advance` retourne le résultat zéro, la suspension originale et cette erreur. `ErrMore` est donc un signal de progrès dans le pas manuel, tandis que `ErrMore` au niveau complétion reste non pris en charge par le `Loop` générique parce que l'opération backend reste vivante.

```go
result, susp := takt.Step[int](exprComputation)
var err error
if susp != nil {
	result, susp, err = takt.Advance(d, susp)
}
// result n'est final que si susp == nil. Sinon, conservez susp et traitez err avant la prochaine étape.
```

Si l’autorisation d’une opération en attente peut être révoquée, appelez `cove.CheckSuspension` avec le contexte courant. En cas de refus, conservez la suspension d’origine. Si la condition est satisfaite, passez la vue à `AdvanceSuspension` et assurez-vous que l’autorisation reste valable pendant l’appel. Après `iox.ErrWouldBlock`, vérifiez-la à nouveau au prochain événement pertinent. [Exemple d’écriture sous condition](write_session_example_test.go)

### Gestion des erreurs

Lorsque le type d’erreur de `Throw` correspond au type attendu, `AdvanceError` abandonne la suspension en attente et renvoie `Left(e)`.

```go
either := takt.ExecError[string](d, computation)
// Right on success, Left on Throw

either, susp := takt.StepError[string, int](exprComputation)
var err error
if susp != nil {
	either, susp, err = takt.AdvanceError[string](d, susp)
}
// either n'est final que si susp == nil. Conservez la suspension en cas de WouldBlock ou d'échec d'infrastructure.
```

Si la suspension d’une vue `cove.SuspensionView[C, kont.Either[E, A]]` est en attente et que la condition est satisfaite, utilisez `either, view.Suspension, err = takt.AdvanceError(d, view.Suspension)`. Lorsque `view.Suspension == nil`, `either` contient le résultat final et `view.Ctx` reste disponible.

### Boucle d'événements

Un `Loop` pilote les calculs au travers d'un `Backend`. Il soumet les opérations, sonde les complétions, les corrèle par `Token` et reprend les continuations suspendues. `NewLoop` accepte des `Option` fonctionnelles. `WithMaxCompletions(n)` panique si `n <= 0` avec `takt: WithMaxCompletions requires n > 0` ; `WithMemory(nil)` panique avec `takt: WithMemory requires a non-nil CompletionMemory`.

`NewLoop` et `NewSubscriptionLoop` exigent une capacité positive. `Submit` et `SubmitExpr` admettent le travail localement ; `Poll` le soumet au backend. Un refus de capacité n'exécute aucun préfixe du programme. `SubmitSuspension` transfère la suspension uniquement en cas de succès. `Ready` distingue le travail nouveau des tentatives différées. Chaque cycle traite le lot avant de reprendre les continuations ; les successeurs attendent un cycle ultérieur. La vidange locale ne libère pas les ressources natives. Voir le [contrat d'exécution](docs/execution.md) et la [portée des preuves](proofs/README.md).

Un programme pur peut terminer immédiatement : vérifiez `done` et la valeur renvoyée par `Submit` ou `SubmitExpr`. `WithMaxCompletions` borne séparément les propositions, les observations et les reprises.

`Backend.Poll([]Completion) (int, error)` rapporte à la fois le nombre de complétions prêtes et tout échec d'infrastructure de sondage ; un backend ne doit pas renvoyer des complétions prêtes avec une erreur de sondage différente de nil. Le `Loop` traite un `iox.ErrWouldBlock` renvoyé par `Poll` comme un tour à vide plutôt que comme une erreur terminale.

Lorsqu'une complétion indique un succès ou un échec ordinaire, `Loop` reprend avec `Completion.ResumeValue()`, y compris les erreurs d'opération encodées dans cette réponse. Les backends habituels remplissent `Value` ; les charges de taille fixe sans pointeurs peuvent utiliser `NewInlineCompletion` et un `CompletionDecoder` pour reporter l'encapsulation dans une interface jusqu'à la reprise. Lire directement `Value` ne décode pas une charge inline ; le décodage peut allouer de la mémoire.

Tous les accès à `Loop` et `SubscriptionLoop` doivent être sérialisés, y compris `Ready`, `Pending` et `Failed`. Les callbacks ne doivent pas réentrer dans les méthodes qui modifient l'état. Ne copiez pas une boucle après sa construction.

`Backend.Submit` doit renvoyer un jeton unique parmi toutes les soumissions encore vivantes dans la boucle. Les jetons sont des clés de corrélation, pas des numéros de séquence ; un backend concret peut utiliser directement une valeur kernel `user_data`. Si un backend réutilise un jeton vivant, la boucle enregistre `ErrLiveTokenReuse`, écarte chaque suspension pendante exactement une fois, puis tout appel ultérieur à `SubmitExpr` / `Submit` / `Poll` / `Run` renvoie cette erreur fatale.

Une complétion portant `iox.ErrWouldBlock` reporte la même suspension à un tour ultérieur, à condition que l'ancienne soumission soit terminée sans réponse ni progrès partiel caché. Une complétion portant `iox.ErrMore` enregistre `ErrUnsupportedMultishot` : l'interaction reste active et nécessite un propriétaire d'abonnement. La boucle traite tout le lot avant de reprendre les réponses. Les réponses stables antérieures au défaut peuvent encore terminer ; leurs suites et les suspensions restantes sont abandonnées selon le contrat de l'adaptateur. `Poll` renvoie d'abord les résultats du préfixe terminé, puis l'erreur conservée lors d'un appel ultérieur ; `Run` renvoie les résultats recueillis avec cette erreur.

Les résultats de `Loop.Poll` sont empruntés jusqu'au prochain `Poll`, `Run` ou `Drain` ; les événements de flux jusqu'au prochain `Poll` ou `Drain`. Copiez les valeurs à conserver. À l'expiration, les références du runner sont effacées, y compris la fin inutilisée du buffer. Cet effacement ne libère pas les ressources : requêtes natives, descripteurs et prêts de buffers gardent leurs propriétaires. Appelez `Loop.Drain` même après une complétion ou un échec pour restituer le slab.

`Loop.Failed()` renvoie l'erreur fatale enregistrée (ou `nil`). `Loop.Drain()` force la boucle dans l'état disposé, écarte chaque suspension pendante exactement une fois et n'enregistre `ErrDisposed` que si aucune erreur fatale n'était déjà présente ; la méthode est idempotente et préserve toute erreur fatale préexistante.

```go
loop := takt.NewLoop[*myBackend, int](backend, 128, takt.WithMaxCompletions(64))
defer loop.Drain()

result, done, err := loop.SubmitExpr(exprComputation)
if err != nil {
	return err
}
if done {
	fmt.Println(result)
} else {
	results, err := loop.Run()
	fmt.Println(results)
	if err != nil {
		return err
	}
}
```

### Exécuteur de stream / abonnement

`SubscriptionLoop` est l'exécuteur frère pour les observations de stream indexées par route. Un `SubscriptionBackend` démarre une opération avec `Subscribe`, sonde des valeurs `StreamCompletion` et accepte les requêtes `Cancel` pour les routes vivantes. Quand toutes les places sont occupées, `Subscribe` renvoie `iox.ErrWouldBlock` avant d’appeler le backend. Chaque `Subscription` appartient à la boucle qui l’a émise. Lorsqu’il réutilise un `Token`, le backend doit changer la génération du `RouteID` tant que la boucle peut encore recevoir un ancien handle ou une ancienne complétion.

`StreamCompletion.More` signifie que la même route reste vivante après l'observation. `HasValue` et `EventErr` décrivent la preuve de charge utile à cette frontière et sont indépendants de `More` : une complétion peut signaler une valeur et d'autres observations à venir, aucune valeur et d'autres observations à venir, ou une erreur de charge utile alors que la route reste vivante. Une complétion avec `More == false` émet un `StreamEvent` final et retire la route.

`Subscribe` rejette le `RouteID` zéro avec `ErrInvalidRouteID` et l'aliasing de route vivante avec `ErrLiveRouteReuse` ; les deux conditions placent la boucle de stream dans un état fatal. Un `iox.ErrWouldBlock` au niveau du sondage est un tour à vide. Un `SubscriptionBackend` ne doit pas renvoyer des complétions de stream prêtes avec une erreur de sondage différente de nil ; les erreurs de charge utile appartiennent à `StreamCompletion.EventErr`. Les complétions de routes inconnues ou déjà retirées sont des observations périmées et sont ignorées. `Cancel` demande l'annulation de la route sans la retirer immédiatement ; une complétion terminale, `Drain` ou une transition fatale de la boucle la retire.

```go
type myStreamBackend struct{ /* ... */ }

func (b *myStreamBackend) Subscribe(op kont.Operation) (takt.RouteID, error) {
	return takt.NewRouteID(tok, generation), nil
}

func (b *myStreamBackend) Poll(out []takt.StreamCompletion[int]) (int, error) {
	// Remplir out avec des observations indexées par route.
	return n, nil
}

func (b *myStreamBackend) Cancel(id takt.RouteID) error {
	// Demander l'annulation de la route vivante.
	return nil
}

stream := takt.NewSubscriptionLoop[*myStreamBackend, int](
	backend, 128,
	takt.WithMaxStreamCompletions(64),
)
defer stream.Drain()

sub, err := stream.Subscribe(op)
if err != nil {
	return err
}
events, err := stream.Poll()
if err != nil {
	return err
}
fmt.Println(sub.ID(), events)
```

### Mémoire des complétions

`NewLoop` utilise [`HeapMemory`](completion_memory.go) comme fournisseur par défaut du buffer de complétion. Si vous voulez que ces buffers proviennent d'un pool borné et stable de slabs de 128 KiB de taille par défaut, passez [`BoundedMemory`](completion_memory.go) via [`WithMemory`](option.go) ; ou fournissez n'importe quelle implémentation de `CompletionMemory` pour contrôler la stratégie d'allocation sans élargir les contrats de `Backend` ni de `Completion`. Les fournisseurs personnalisés doivent renvoyer des slabs vivants exclusifs et non chevauchants, et peuvent traiter `Release` comme un transfert de propriété vers le fournisseur :

```go
// Par défaut : HeapMemory + slab de complétion de taille par défaut.
defaultLoop := takt.NewLoop[*myBackend, int](backend, 128)
defer defaultLoop.Drain()

// Limite la longueur du slab de complétion par scrutation (le fournisseur choisit la forme du slab ; Loop tronque la longueur visible à ce plafond).
cappedLoop := takt.NewLoop[*myBackend, int](backend, 128, takt.WithMaxCompletions(64))
defer cappedLoop.Drain()

// Partagez le même pointeur HeapMemory entre boucles. Ne copiez ni HeapMemory ni BoundedMemory après leur première utilisation.
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backendA, 128, takt.WithMemory(heap))
defer loopA.Drain()
loopB := takt.NewLoop[*myBackend, int](backendB, 128, takt.WithMemory(heap))
defer loopB.Drain()

// BoundedMemory : un pool borné de slabs de 128 KiB de taille par défaut. WithPoolCapacity ajuste la capacité de ce pool (iobuf l'arrondit à la puissance de deux supérieure).
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
boundedLoop := takt.NewLoop[*myBackend, int](
	backend, 128,
	takt.WithMemory(bounded),
	takt.WithMaxCompletions(64),
)
defer boundedLoop.Drain()
```

`BoundedMemory` limite le stockage conservé dans le pool, pas toute la mémoire utilisée. Si le pool est épuisé, un slab supplémentaire de taille par défaut est alloué ; les demandes plus grandes allouent des slabs de taille exacte. `Release` ne conserve pas ces slabs supplémentaires. Appelez `Drain` sur chaque boucle pour rendre son stockage, même après la fin du calcul ou un échec ; la libération des ressources natives reste à la charge de l'intégration.

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
- `Completion`: token, résultat et charge de réponse directe ou inline
- `Completion.ResumeValue() kont.Resumed`: obtient la réponse dans l'une ou l'autre représentation
- `CompletionDecoder`: décode deux mots sans pointeurs lors de la reprise
- `NewInlineCompletion(token Token, word0, word1 uint64, decoder CompletionDecoder, err error) Completion`: construit une complétion inline
- `NewLoop[B, R](b B, capacity int, opts ...Option) *Loop[B, R]`: crée une boucle d'événements (avec `HeapMemory` et un slab par défaut si rien n'est précisé)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)`: réserve et évalue ; résultat immédiat ou suspension en file
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)`: réserve et réifie ; résultat immédiat ou suspension en file
- `(*Loop[B, R]).Poll() ([]R, error)`: sonde et dispatche les complétions
- `(*Loop[B, R]).Run() ([]R, error)`: fait avancer le tout jusqu'à complétion
- `(*Loop[B, R]).SubmitSuspension(s *kont.Suspension[R]) error`
- `(*Loop[B, R]).Ready() bool`
- `(*Loop[B, R]).Pending() int`: lignées de calcul locales, y compris les tentatives différées
- `(*Loop[B, R]).Failed() error`: erreur fatale terminale, ou nil
- `(*Loop[B, R]).Drain() int`: abandonne les suspensions en attente et dispose la boucle
- `ErrLiveTokenReuse`: le backend a réutilisé un jeton encore vivant dans la boucle
- `ErrUnsupportedMultishot`: une complétion multishot n'est pas prise en charge par le `Loop` générique
- `ErrDisposed`: la boucle a été disposée via `Drain`

### Routes de stream

- `RouteID`: identité de route de stream (`Token` plus génération)
- `NewRouteID(token Token, generation uint64) RouteID`: construit un identifiant de route pour les backends de stream
- `RouteID.Token() Token`: composant token
- `RouteID.Generation() uint64`: composant génération
- `RouteID.IsZero() bool`: test de route réservée invalide
- `Subscription[A]`: identité opaque de route de stream
- `Subscription[A].ID() RouteID`: identité de route portée par le descripteur
- `Subscription[A].IsZero() bool`: test du descripteur zéro ; ne consulte pas l'état actif de la route
- `StreamCompletion[A]`: `{ID, Value, HasValue, EventErr, More}`
- `StreamCompletion[A].RouteOutcome() iox.Outcome`: projection vers le vocabulaire de résultats `iox`
- `StreamEvent[A]`: `{Subscription, Value, HasValue, Final, EventErr}`
- `SubscriptionBackend[B, A]`: interface abstraite de backend de stream (`Subscribe`, `Poll`, `Cancel`)
- `SubscriptionOption`: options fonctionnelles pour `NewSubscriptionLoop`
- `WithMaxStreamCompletions(n)`: plafonne le buffer de complétions de stream par sondage
- `NewSubscriptionLoop[B, A](b B, capacity int, opts ...SubscriptionOption) *SubscriptionLoop[B, A]`: crée un exécuteur de stream
- `(*SubscriptionLoop[B, A]).Subscribe(op kont.Operation) (Subscription[A], error)`: démarre une opération productrice de route
- `(*SubscriptionLoop[B, A]).Poll() ([]StreamEvent[A], error)`: sonde les événements indexés par route
- `(*SubscriptionLoop[B, A]).Cancel(sub Subscription[A]) error`: demande l'annulation de route
- `(*SubscriptionLoop[B, A]).Pending() int`: compte les routes de stream vivantes
- `(*SubscriptionLoop[B, A]).Failed() error`: erreur fatale terminale, ou nil
- `(*SubscriptionLoop[B, A]).Drain() int`: annule ou retire les routes possédées et met au rebut la boucle de stream
- `ErrLiveRouteReuse`: le backend a réutilisé une route encore vivante
- `ErrInvalidRouteID`: le backend a renvoyé le `RouteID` zéro réservé
- `ErrUnknownSubscription`: le descripteur de souscription n'est pas vivant dans la boucle de stream

### Pont

- `Reify[A](kont.Eff[A]) kont.Expr[A]`: Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]`: Expr → Cont

## Schémas pratiques

Utilisez un `Backend` avec `Loop`, ou un `Dispatcher` avec `Exec` et `Advance`. Ce sont des chemins distincts : Loop n'appelle pas automatiquement Dispatcher. `ExecError` et `AdvanceError` interprètent explicitement les effets d'erreur d'application. Un Loop brut exige un backend/interpréteur qui traite toutes les opérations admises, effets d'erreur compris. Les [exemples exécutables](example_test.go) montrent les suspensions suivantes, les résultats immédiats et l'admission différée au backend.

Le guide de [vérification](docs/verification.md) décrit les contrôles et la portée des preuves. Le guide de [performance](docs/performance.md) couvre la localité, l'ordonnancement, l'attente adaptative, la répartition typée et les mesures reproductibles.

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
