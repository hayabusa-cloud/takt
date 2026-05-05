[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | **Español** | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

Motor de despacho abstracto, guiado por eventos de finalización, para pilas de E/S no bloqueantes.

## Descripción general

En un modelo proactor, las operaciones de E/S se envían al kernel y sus finalizaciones llegan de forma asíncrona. La aplicación debe correlacionar cada finalización con la computación que la solicitó, reanudar esa computación y manejar éxito, progreso con una frontera viva, disponibilidad/contrapresión sin progreso y fallo.

`takt` proporciona este modelo de ejecución como una capa abstracta sobre el sistema de efectos [kont](https://code.hybscloud.com/kont). Un `Dispatcher` evalúa un efecto algebraico a la vez y clasifica el resultado según el álgebra de resultados [iox](https://code.hybscloud.com/iox). Un `Backend` envía operaciones a un motor asíncrono, por ejemplo `io_uring`, y sondea las finalizaciones. `Loop` los integra: envía computaciones, sondea el backend, correlaciona finalizaciones por token y reanuda las continuaciones suspendidas.

Hay dos API equivalentes: `kont.Eff` (basada en clausuras y fácil de componer) y `kont.Expr` (basada en marcos, con menor sobrecarga de asignación en rutas críticas).

La ruta del bucle de eventos guarda una sola suspensión pendiente por cada token vivo. Cada token sigue una suspensión producida por `kont.StepExpr` (o por reificar antes `kont.Eff`), por lo que `Backend.Submit` no debe reutilizar un token mientras el envío anterior que lo porta siga vivo en el bucle.

Para integraciones de estilo stream o multishot, `SubscriptionLoop` proporciona un ejecutor abstracto de rutas separado. Rastrea valores `Subscription` por `RouteID` (`Token` más generación), sondea valores `StreamCompletion`, emite observaciones `StreamEvent` y conserva `More` como evidencia de vida de la ruta independiente del valor de carga o del error de carga. Así el `Loop` genérico sigue siendo afín y de un solo disparo, mientras los entornos de ejecución concretos tienen un lugar explícito para representar observaciones sucesoras de la misma operación.

## Límite de composición

`takt` es dueño del movimiento de ejecución, no del significado del contexto ni del vocabulario de resultados. `iox` clasifica `nil`, `ErrWouldBlock`, `ErrMore` y los fallos; `kont` posee el portador de suspensión/reanudación; `cove` puede envolver una suspensión con contexto explícito mediante `SuspensionView`; `takt` avanza cualquier valor que satisfaga `SuspensionLike` sin interpretar ese contexto.

## Instalación

```bash
go get code.hybscloud.com/takt
```

Requiere Go 1.26+.

## Clasificación de resultados

Cada operación despachada devuelve un resultado `iox`. `Dispatcher.Dispatch` informa ese resultado como `(value, error)`; los ejecutores bloqueantes y la API de stepping lo interpretan así:

| Resultado       | Significado                      | Retorno Dispatcher      | Comportamiento bloqueante/stepping          |
|-----------------|----------------------------------|-------------------------|---------------------------------------------|
| `nil`           | completado                       | `(value, nil)`          | reanuda                                     |
| `ErrMore`       | progreso con frontera viva       | `(value, ErrMore)`      | reanuda; stepping devuelve `ErrMore`        |
| `ErrWouldBlock` | sin progreso                     | `(nil, ErrWouldBlock)`  | el bloqueante espera; stepping devuelve la suspensión |
| otro            | fallo de infraestructura         | `(nil, error)`          | el bloqueante hace panic; stepping devuelve el error |

## Uso

### Dispatcher

Un `Dispatcher` mapea cada efecto algebraico a una operación de E/S concreta y devuelve el resultado junto con su estado `iox`.

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	// despachar op; devolver (value, nil), (value, iox.ErrMore) o (nil, iox.ErrWouldBlock)
}
```

### Evaluación bloqueante

`Exec` y `ExecExpr` ejecutan una computación hasta completarla, esperando de forma síncrona cuando el dispatcher devuelve `iox.ErrWouldBlock`.

```go
result := takt.Exec(d, computation) // kont.Eff
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### Paso a paso

Para bucles de eventos proactor, por ejemplo `io_uring`, `Step` y `Advance` evalúan un efecto a la vez. Cuando el dispatcher devuelve `iox.ErrWouldBlock`, la suspensión se devuelve al llamador, permitiendo al bucle de eventos reprogramar.

La ley de resultados del stepping manual es explícita: si `d.Dispatch(susp.Op())` devuelve `nil` o `iox.ErrMore`, `Advance` reanuda la suspensión con el valor devuelto y devuelve la siguiente suspensión junto con el error original; si devuelve `iox.ErrWouldBlock` o un fallo ordinario, `Advance` devuelve el resultado cero, la suspensión original y ese error. `ErrMore` es por tanto una señal de progreso en stepping manual, mientras que `ErrMore` a nivel de finalización sigue sin estar soportado por el `Loop` genérico porque la operación de backend continúa viva.

```go
result, susp := takt.Step[int](exprComputation)
if susp != nil {
	var err error
	result, susp, err = takt.Advance(d, susp)
	if iox.IsWouldBlock(err) {
		return susp // ceder al bucle de eventos; reprogramar cuando esté listo
	}
}
// result contiene el valor final
```

### Manejo de errores

Componga operaciones del dispatcher con efectos de error. `Throw` cortocircuita la computación de forma inmediata y descarta la suspensión pendiente.

```go
either := takt.ExecError[string](d, computation)
// Right en caso de éxito; Left en caso de Throw

// Paso a paso con errores
either, susp := takt.StepError[string, int](exprComputation)
if susp != nil {
	var err error
	either, susp, err = takt.AdvanceError[string](d, susp)
	if iox.IsWouldBlock(err) {
		return susp // ceder al bucle de eventos; reprogramar cuando esté listo
	}
}
```

### Bucle de eventos

Un `Loop` conduce computaciones a través de un `Backend`. Envía operaciones, sondea finalizaciones, las correlaciona por `Token` y reanuda las continuaciones suspendidas. `NewLoop` acepta `Option` funcionales. `WithMaxCompletions(n)` entra en pánico si `n <= 0` con `takt: WithMaxCompletions requires n > 0`; `WithMemory(nil)` entra en pánico con `takt: WithMemory requires a non-nil CompletionMemory`.

`Backend.Poll([]Completion) (int, error)` informa tanto el número de finalizaciones listas como cualquier fallo de infraestructura del sondeo; un backend no debe devolver finalizaciones listas junto con un error de sondeo distinto de nil. El `Loop` trata un `iox.ErrWouldBlock` devuelto por `Poll` como un ciclo en vacío y no como un error terminal.

`Loop` es un ejecutor de propietario único. Serialice las llamadas que comparten el mismo `Loop`, incluidas `SubmitExpr`, `Submit`, `Poll`, `Run`, `Drain`, `Pending` y `Failed`.

`Backend.Submit` debe devolver un token que sea único entre todos los envíos que sigan vivos en el bucle. Los tokens son claves de correlación, no números de secuencia; un backend concreto puede usar directamente un valor kernel `user_data`. Si un backend reutiliza un token vivo, el bucle registra `ErrLiveTokenReuse`, descarta cada suspensión pendiente exactamente una vez y toda llamada posterior a `SubmitExpr` / `Submit` / `Poll` / `Run` devuelve ese error fatal.

Cuando una finalización trae `iox.ErrWouldBlock`, el bucle reenvía la misma operación. Si una finalización trae `iox.ErrMore` (multishot), el bucle registra `ErrUnsupportedMultishot`, descarta cada suspensión pendiente exactamente una vez y toda llamada posterior a `SubmitExpr` / `Submit` / `Poll` / `Run` devuelve ese error fatal. `ErrMore` significa que la operación de backend enviada sigue activa después de la CQE, mientras que el `Loop` genérico no tiene un portador de suscripción o cancelación para finalizaciones posteriores con el mismo token.

`Loop.Failed()` reporta el error fatal registrado (o `nil`). `Loop.Drain()` fuerza al bucle al estado desechado, descarta toda suspensión pendiente exactamente una vez y registra `ErrDisposed` solo si antes no había un error fatal; es idempotente y preserva el fatal original cuando ya existe.

```go
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// Enviar computaciones
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // kont.Eff

// Conducir todo hasta completar
results, err := loop.Run()
```

### Ejecutor de stream / suscripción

`SubscriptionLoop` es el ejecutor hermano para observaciones de stream indexadas por ruta. Un `SubscriptionBackend` inicia una operación con `Subscribe`, sondea valores `StreamCompletion` y acepta solicitudes `Cancel` para rutas vivas. `RouteID` empareja un `Token` con una generación para que la reutilización de tokens tras la finalización no se confunda con una ruta viva anterior.

`StreamCompletion.More` significa que la misma ruta permanece viva después de la observación. `HasValue` y `EventErr` describen evidencia de carga en ese límite y son independientes de `More`: una finalización puede informar un valor y más por venir, ningún valor y más por venir, o un error de carga mientras la ruta sigue viva. Una finalización con `More == false` emite un `StreamEvent` final y retira la ruta.

`Subscribe` rechaza el `RouteID` cero con `ErrInvalidRouteID` y el aliasing de rutas vivas con `ErrLiveRouteReuse`; ambas condiciones ponen el bucle de stream en estado fatal. Un `iox.ErrWouldBlock` a nivel de sondeo es un ciclo en vacío. Un `SubscriptionBackend` no debe devolver finalizaciones de stream listas junto con un error de sondeo distinto de nil; los errores de carga pertenecen a `StreamCompletion.EventErr`. Las finalizaciones de rutas desconocidas o ya retiradas son observaciones obsoletas y se ignoran. `Cancel` solicita la cancelación de la ruta sin retirarla de inmediato; una finalización terminal, `Drain` o una transición fatal del bucle la retira.

```go
type myStreamBackend struct{ /* ... */ }

func (b *myStreamBackend) Subscribe(op kont.Operation) (takt.RouteID, error) {
	return takt.NewRouteID(tok, generation), nil
}

func (b *myStreamBackend) Poll(out []takt.StreamCompletion[int]) (int, error) {
	// Rellenar out con observaciones indexadas por ruta.
	return n, nil
}

func (b *myStreamBackend) Cancel(id takt.RouteID) error {
	// Solicitar cancelación de la ruta viva.
	return nil
}

stream := takt.NewSubscriptionLoop[*myStreamBackend, int](
	backend,
	takt.WithMaxStreamCompletions(64),
)

sub, err := stream.Subscribe(op)
events, err := stream.Poll()
_ = sub
_ = events
_ = err
```

`NewLoop` usa [`HeapMemory`](completion_memory.go) como proveedor predeterminado del búfer de finalizaciones. Si quiere que esos búferes provengan de un pool acotado y estable de slabs de 128 KiB de tamaño predeterminado, pase [`BoundedMemory`](completion_memory.go) mediante [`WithMemory`](option.go); o proporcione cualquier implementación de `CompletionMemory` para controlar la estrategia de asignación sin ampliar los contratos de `Backend` ni de `Completion`. Los proveedores personalizados deben devolver slabs vivos exclusivos y no solapados, y pueden tratar `Release` como una transferencia de propiedad de vuelta al proveedor:

```go
// Por defecto: HeapMemory + slab de finalizaciones de tamaño por defecto.
loop := takt.NewLoop[*myBackend, int](backend)

// Limita la longitud del slab de finalizaciones por sondeo (el proveedor elige la forma del slab; Loop recorta la longitud visible a este tope).
loop = takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// Comparte el sync.Pool de un mismo HeapMemory entre varios Loops pasando la misma dirección a WithMemory; copiar un valor HeapMemory no compartiría los slabs reciclados.
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))
loopB := takt.NewLoop[*myBackend, int](backend, takt.WithMemory(heap))

// BoundedMemory: un pool acotado de slabs de 128 KiB con tamaño por defecto. WithPoolCapacity ajusta la capacidad de ese pool (iobuf la redondea a la siguiente potencia de dos).
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
loop = takt.NewLoop[*myBackend, int](
	backend,
	takt.WithMemory(bounded),
	takt.WithMaxCompletions(64),
)
```

## Resumen de API

### Despacho

- `Dispatcher[D Dispatcher[D]]`: interfaz de despacho no bloqueante
- `Exec[D, R](d D, m kont.Eff[R]) R`: evaluación bloqueante de `kont.Eff`
- `ExecExpr[D, R](d D, m kont.Expr[R]) R`: evaluación bloqueante de `kont.Expr`

### Paso a paso

- `SuspensionLike[S, R]`: interfaz reanudable con `Op` y `Resume`, implementada por `cove.SuspensionView`
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])`: evalúa hasta la primera suspensión
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)`: despacha una operación a través de cualquier valor `SuspensionLike`
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)`: despacha una operación

### Manejo de errores

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]`: bloqueante con errores
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]`: evaluación de `kont.Expr` con manejo de errores
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])`: paso a paso con errores
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)`: avance con errores

### Backend y bucle de eventos

- `Backend[B Backend[B]]`: interfaz asíncrona de submit/poll
- `CompletionMemory`: proveedor local del búfer de finalizaciones del bucle
- `HeapMemory`: implementación predeterminada (`sync.Pool` con slabs tipados de tamaño predeterminado)
- `BoundedMemory`: implementación basada en iobuf con un único pool acotado de slabs de 128 KiB de tamaño predeterminado
- `Option`: opción funcional para `NewLoop` (`WithMemory`, `WithMaxCompletions`)
- `CompletionBufOption`: opción funcional para `CompletionMemory.CompletionBuf` (`WithSize`)
- `BoundedMemoryOption`: opción funcional para `NewBoundedMemory` (`WithPoolCapacity`)
- `Token`: correlación envío-finalización (`uint64`)
- `Completion`: `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, opts ...Option) *Loop[B, R]`: crea un bucle de eventos (`HeapMemory` y slab predeterminado por defecto)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)`: avanza un paso y envía un Expr
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)`: avanza un paso y envía un `kont.Eff`
- `(*Loop[B, R]).Poll() ([]R, error)`: sondea y despacha finalizaciones
- `(*Loop[B, R]).Run() ([]R, error)`: conduce todo hasta completar
- `(*Loop[B, R]).Pending() int`: número de operaciones pendientes
- `(*Loop[B, R]).Failed() error`: error fatal terminal, o nil
- `(*Loop[B, R]).Drain() int`: descarta suspensiones pendientes y desecha el bucle
- `ErrLiveTokenReuse`: el backend reutilizó un token que seguía vivo en el bucle
- `ErrUnsupportedMultishot`: el `Loop` genérico no admite finalizaciones multishot
- `ErrDisposed`: el bucle ha sido desechado mediante `Drain`

### Rutas de stream

- `RouteID`: identidad de ruta de stream (`Token` más generación)
- `NewRouteID(token Token, generation uint64) RouteID`: construye un identificador de ruta para backends de stream
- `RouteID.Token() Token`: componente token
- `RouteID.Generation() uint64`: componente generación
- `RouteID.IsZero() bool`: prueba de ruta reservada inválida
- `Subscription[A]`: descriptor opaco de stream vivo
- `Subscription[A].ID() RouteID`: identidad de ruta transportada por el descriptor
- `Subscription[A].IsZero() bool`: prueba de descriptor cero y no vivo
- `StreamCompletion[A]`: `{ID, Value, HasValue, EventErr, More}`
- `StreamCompletion[A].RouteOutcome() iox.Outcome`: proyección al vocabulario de resultados `iox`
- `StreamEvent[A]`: `{Subscription, Value, HasValue, Final, EventErr}`
- `SubscriptionBackend[B, A]`: interfaz abstracta de backend de stream (`Subscribe`, `Poll`, `Cancel`)
- `SubscriptionOption`: opciones funcionales para `NewSubscriptionLoop`
- `WithMaxStreamCompletions(n)`: acota el búfer de finalizaciones de stream por sondeo
- `NewSubscriptionLoop[B, A](b B, opts ...SubscriptionOption) *SubscriptionLoop[B, A]`: crea un ejecutor de stream
- `(*SubscriptionLoop[B, A]).Subscribe(op kont.Operation) (Subscription[A], error)`: inicia una operación que produce una ruta
- `(*SubscriptionLoop[B, A]).Poll() ([]StreamEvent[A], error)`: sondea eventos indexados por ruta
- `(*SubscriptionLoop[B, A]).Cancel(sub Subscription[A]) error`: solicita cancelación de ruta
- `(*SubscriptionLoop[B, A]).Pending() int`: cuenta rutas de stream vivas
- `(*SubscriptionLoop[B, A]).Failed() error`: error fatal terminal, o nil
- `(*SubscriptionLoop[B, A]).Drain() int`: cancela o retira rutas propias y desecha el bucle de stream
- `ErrLiveRouteReuse`: el backend reutilizó una ruta que seguía viva
- `ErrInvalidRouteID`: el backend devolvió el `RouteID` cero reservado
- `ErrUnknownSubscription`: el descriptor de suscripción no está vivo en el bucle de stream

### Puente

- `Reify[A](kont.Eff[A]) kont.Expr[A]`: Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]`: Expr → Cont

## Patrones prácticos

Una integración completa con un bucle de eventos combina un `Dispatcher` (la semántica síncrona) con un `Backend` (el proactor) bajo una misma `Loop`:

```go
// 1. Definir el dispatcher: mapea una operación de efecto a un resultado iox.
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
	// Devolver (value, nil), (value, iox.ErrMore) o (nil, iox.ErrWouldBlock).
}

// 2. Definir el backend: envía operaciones al proactor del SO y sondea finalizaciones.
type myBackend struct{ /* ... */ }
func (b *myBackend) Submit(op kont.Operation) (takt.Token, error) { /* ... */ }
func (b *myBackend) Poll(out []takt.Completion) (int, error)      { /* ... */ }

// 3. Conducir: enviar una o más computaciones y luego Run hasta completar.
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))
loop.SubmitExpr(prog1)
loop.SubmitExpr(prog2)
results, err := loop.Run()
_ = results; _ = err
```

Para composición con manejo de errores, use `ExecError` / `StepError` / `AdvanceError` en lugar de sus equivalentes sin error; `Throw` cortocircuita la computación en curso sin afectar a las computaciones hermanas del mismo bucle. El patrón fusionado dispatcher+backend mostrado aquí es precisamente el que usa `sess` para conectar un endpoint de sesión a un runtime de E/S real.

## Referencias

- Tarmo Uustalu and Varmo Vene. 2008. Comonadic Notions of Computation. *Electronic Notes in Theoretical Computer Science* 203, 5 (June 2008), 263–284. https://doi.org/10.1016/j.entcs.2008.05.029
- Gordon D. Plotkin and Matija Pretnar. 2009. Handlers of Algebraic Effects. In *Proc. 18th European Symposium on Programming (ESOP '09)*. LNCS 5502, 80–94. https://doi.org/10.1007/978-3-642-00590-9_7
- Andrej Bauer and Matija Pretnar. 2015. Programming with Algebraic Effects and Handlers. *Journal of Logical and Algebraic Methods in Programming* 84, 1 (Jan. 2015), 108–123. https://arxiv.org/abs/1203.1539
- Daniel Leijen. 2017. Type Directed Compilation of Row-Typed Algebraic Effects. In *Proc. 44th ACM SIGPLAN Symposium on Principles of Programming Languages (POPL '17)*. 486–499. https://doi.org/10.1145/3009837.3009872
- Danel Ahman and Andrej Bauer. 2020. Runners in Action. In *Proc. 29th European Symposium on Programming (ESOP '20)*. LNCS 12075, 29–55. https://arxiv.org/abs/1910.11629
- Daniel Hillerström, Sam Lindley, and Robert Atkey. 2020. Effect Handlers via Generalised Continuations. *Journal of Functional Programming* 30 (2020), e5. https://bentnib.org/handlers-cps-journal.pdf

## Licencia

Licencia MIT. Consulte [LICENSE](LICENSE) para más detalles.

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
