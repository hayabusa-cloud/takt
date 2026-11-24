[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Benchmarks](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml/badge.svg)](https://github.com/hayabusa-cloud/takt/actions/workflows/benchmarks.yml)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | **Español** | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

Motor de despacho abstracto, guiado por eventos de finalización, para pilas de E/S no bloqueantes.

## Descripción general

En un modelo proactor, las operaciones de E/S se envían al kernel y sus finalizaciones llegan de forma asíncrona. La aplicación debe correlacionar cada finalización con la computación que la solicitó, reanudar esa computación y manejar éxito, progreso con una frontera viva, disponibilidad/contrapresión sin progreso y fallo.

`takt` proporciona este modelo de ejecución como una capa abstracta sobre el sistema de efectos [kont](https://code.hybscloud.com/kont). Un `Dispatcher` evalúa un efecto algebraico a la vez y clasifica el resultado según el álgebra de resultados [iox](https://code.hybscloud.com/iox). Un `Backend` envía operaciones a un motor asíncrono, por ejemplo `io_uring`, y sondea las finalizaciones. `Loop` los integra: envía computaciones, sondea el backend, correlaciona finalizaciones por token y reanuda las continuaciones suspendidas.

Los programas tienen dos representaciones: `kont.Eff` usa clausuras para la composición y `kont.Expr` expone marcos para avanzar por pasos. `Reify` y `Reflect` las conectan. El coste de asignación y ejecución depende del programa y su realización; la representación por sí sola no garantiza una ruta crítica más barata.

La ruta del bucle de eventos guarda una sola suspensión pendiente por cada token vivo. Cada token sigue una suspensión producida por `kont.StepExpr` (o por reificar antes `kont.Eff`), por lo que `Backend.Submit` no debe reutilizar un token mientras el envío anterior que lo porta siga vivo en el bucle.

Para integraciones de estilo stream o multishot, `SubscriptionLoop` proporciona un ejecutor abstracto de rutas separado. Rastrea valores `Subscription` por `RouteID` (`Token` más generación), sondea valores `StreamCompletion`, emite observaciones `StreamEvent` y conserva `More` como evidencia de vida de la ruta independiente del valor de carga o del error de carga. Así el `Loop` genérico sigue siendo afín y de un solo disparo, mientras los entornos de ejecución concretos tienen un lugar explícito para representar observaciones sucesoras de la misma operación.

## Límite de composición

`takt` es dueño del movimiento de ejecución, no del significado del contexto ni del vocabulario de resultados. `iox` clasifica `nil`, `ErrWouldBlock`, `ErrMore` y los fallos; `kont` posee el portador de suspensión/reanudación; `cove` puede envolver una suspensión con contexto explícito mediante `SuspensionView`; `takt` avanza cualquier valor que satisfaga `SuspensionLike` sin interpretar ese contexto.

## Instalación

```bash
go get code.hybscloud.com/takt
```

Requiere Go 1.27+.

## Clasificación de resultados

Cada operación despachada devuelve un resultado `iox`. `Dispatcher.Dispatch` informa ese resultado como `(value, error)`; los ejecutores bloqueantes y la API de stepping lo interpretan así:

| Resultado       | Significado                      | Retorno Dispatcher      | Comportamiento bloqueante/stepping          |
|-----------------|----------------------------------|-------------------------|---------------------------------------------|
| `nil`           | respuesta válida | `(value, nil)`          | reanuda                                     |
| `ErrMore`       | respuesta válida; interacción viva | `(value, ErrMore)`      | reanuda; stepping devuelve `ErrMore`        |
| `ErrWouldBlock` | sin respuesta ahora; reintentar después | `(nil, ErrWouldBlock)`  | el bloqueante espera; stepping devuelve la suspensión |
| otro            | fallo de infraestructura         | `(nil, error)`          | el bloqueante hace panic; stepping devuelve el error |

La tabla describe el contrato de respuesta de Dispatcher. El control de `iox` no mide bytes transferidos ni demuestra la liberación de recursos; el dispatcher debe conservar el trabajo parcial al devolver WouldBlock.

El despacho sigue `iox.Classify`, incluidos los errores envueltos. Un error que coincide con `ErrWouldBlock` y `ErrMore` se clasifica como WouldBlock: se conserva la suspensión para reintentar después.

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
var err error
if susp != nil {
	result, susp, err = takt.Advance(d, susp)
}
// result solo es final cuando susp == nil. En otro caso, conserve susp y gestione err antes de avanzar.
```

Si el permiso de una operación pendiente puede revocarse, llame a `cove.CheckSuspension` con el contexto actual. Si la comprobación falla, conserve la suspensión original. Si se cumple la condición, pase la vista a `AdvanceSuspension` y asegúrese de que el permiso siga vigente durante la llamada. Tras `iox.ErrWouldBlock`, vuelva a comprobarlo cuando llegue el siguiente evento pertinente. [Ejemplo de escritura con permiso](write_session_example_test.go)

### Manejo de errores

Cuando el tipo de error de `Throw` coincide con el esperado, `AdvanceError` descarta la suspensión pendiente y devuelve `Left(e)`.

```go
either := takt.ExecError[string](d, computation)
// Right on success, Left on Throw

either, susp := takt.StepError[string, int](exprComputation)
var err error
if susp != nil {
	either, susp, err = takt.AdvanceError[string](d, susp)
}
// either solo es final cuando susp == nil. Conserve la suspensión ante WouldBlock o fallo de infraestructura.
```

Si la suspensión de una vista `cove.SuspensionView[C, kont.Either[E, A]]` está pendiente y se cumple la condición, use `either, view.Suspension, err = takt.AdvanceError(d, view.Suspension)`. Cuando `view.Suspension == nil`, `either` contiene el resultado final y `view.Ctx` sigue disponible.

### Bucle de eventos

Un `Loop` conduce computaciones a través de un `Backend`. Envía operaciones, sondea finalizaciones, las correlaciona por `Token` y reanuda las continuaciones suspendidas. `NewLoop` acepta `Option` funcionales. `WithMaxCompletions(n)` entra en pánico si `n <= 0` con `takt: WithMaxCompletions requires n > 0`; `WithMemory(nil)` entra en pánico con `takt: WithMemory requires a non-nil CompletionMemory`.

`NewLoop` y `NewSubscriptionLoop` requieren una capacidad positiva. `Submit` y `SubmitExpr` admiten trabajo localmente; `Poll` lo envía al backend. El rechazo por capacidad no ejecuta el prefijo del programa. `SubmitSuspension` transfiere la suspensión solo si tiene éxito. `Ready` distingue trabajo nuevo de reintentos diferidos. Cada ciclo procesa el lote antes de reanudar; los sucesores se envían en ciclos posteriores. El vaciado local no libera recursos nativos. Véanse el [contrato de ejecución](docs/execution.md) y el [alcance de las pruebas formales](proofs/README.md).

Un programa puro puede terminar de inmediato: compruebe `done` y el valor devuelto por `Submit` o `SubmitExpr`. `WithMaxCompletions` limita por separado ofertas, observaciones y reanudaciones.

`Backend.Poll([]Completion) (int, error)` informa tanto el número de finalizaciones listas como cualquier fallo de infraestructura del sondeo; un backend no debe devolver finalizaciones listas junto con un error de sondeo distinto de nil. El `Loop` trata un `iox.ErrWouldBlock` devuelto por `Poll` como un ciclo en vacío y no como un error terminal.

Cuando una finalización indica éxito o un fallo ordinario, `Loop` reanuda con `Completion.ResumeValue()`, incluidos los errores de operación codificados en esa respuesta. Los backends habituales rellenan `Value`; las cargas de tamaño fijo sin punteros pueden usar `NewInlineCompletion` y un `CompletionDecoder` para posponer el empaquetado en una interfaz hasta la reanudación. Leer `Value` directamente no decodifica cargas inline; la decodificación puede asignar memoria.

Todo acceso a `Loop` y `SubscriptionLoop` debe serializarse, incluidos `Ready`, `Pending` y `Failed`. Los callbacks no deben reentrar en métodos que cambian el estado. No copie un bucle después de construirlo.

`Backend.Submit` debe devolver un token que sea único entre todos los envíos que sigan vivos en el bucle. Los tokens son claves de correlación, no números de secuencia; un backend concreto puede usar directamente un valor kernel `user_data`. Si un backend reutiliza un token vivo, el bucle registra `ErrLiveTokenReuse`, descarta cada suspensión pendiente exactamente una vez y toda llamada posterior a `SubmitExpr` / `Submit` / `Poll` / `Run` devuelve ese error fatal.

Una finalización con `iox.ErrWouldBlock` difiere la misma suspensión a otra ronda, siempre que el envío anterior haya terminado sin respuesta ni progreso parcial oculto. Una finalización con `iox.ErrMore` registra `ErrUnsupportedMultishot`: la interacción sigue viva y necesita un propietario de suscripción. El bucle procesa todo el lote antes de reanudar. Las respuestas estables anteriores al fallo aún pueden completar; sus sucesoras y las suspensiones restantes se descartan según el contrato de abandono del adaptador. `Poll` devuelve primero los resultados del prefijo completado y comunica el error guardado en otra llamada; `Run` devuelve los resultados recopilados junto con el error.

Los resultados de `Loop.Poll` se prestan hasta el siguiente `Poll`, `Run` o `Drain`; los eventos de flujo, hasta el siguiente `Poll` o `Drain`. Copie los valores que necesite conservar. Al vencer el préstamo se borran las referencias del ejecutor, incluida la cola no utilizada del búfer. Borrar referencias no libera recursos: solicitudes nativas, descriptores y préstamos de búfer conservan sus propios propietarios. Llame a `Loop.Drain` incluso después de completar o fallar para devolver el slab de finalizaciones.

`Loop.Failed()` reporta el error fatal registrado (o `nil`). `Loop.Drain()` fuerza al bucle al estado desechado, descarta toda suspensión pendiente exactamente una vez y registra `ErrDisposed` solo si antes no había un error fatal; es idempotente y preserva el fatal original cuando ya existe.

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

### Ejecutor de stream / suscripción

`SubscriptionLoop` es el ejecutor hermano para observaciones de stream indexadas por ruta. Un `SubscriptionBackend` inicia una operación con `Subscribe`, sondea valores `StreamCompletion` y acepta solicitudes `Cancel` para rutas vivas. Si todas las plazas están ocupadas, `Subscribe` devuelve `iox.ErrWouldBlock` antes de llamar al backend. Cada `Subscription` pertenece al bucle que la emitió. Al reutilizar un `Token`, el backend debe usar una nueva generación en el `RouteID` mientras el bucle pueda recibir un identificador o una finalización anteriores.

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

### Memoria de finalizaciones

`NewLoop` usa [`HeapMemory`](completion_memory.go) como proveedor predeterminado del búfer de finalizaciones. Si quiere que esos búferes provengan de un pool acotado y estable de slabs de 128 KiB de tamaño predeterminado, pase [`BoundedMemory`](completion_memory.go) mediante [`WithMemory`](option.go); o proporcione cualquier implementación de `CompletionMemory` para controlar la estrategia de asignación sin ampliar los contratos de `Backend` ni de `Completion`. Los proveedores personalizados deben devolver slabs vivos exclusivos y no solapados, y pueden tratar `Release` como una transferencia de propiedad de vuelta al proveedor:

```go
// Por defecto: HeapMemory + slab de finalizaciones de tamaño por defecto.
defaultLoop := takt.NewLoop[*myBackend, int](backend, 128)
defer defaultLoop.Drain()

// Limita la longitud del slab de finalizaciones por sondeo (el proveedor elige la forma del slab; Loop recorta la longitud visible a este tope).
cappedLoop := takt.NewLoop[*myBackend, int](backend, 128, takt.WithMaxCompletions(64))
defer cappedLoop.Drain()

// Comparta el mismo puntero HeapMemory entre bucles. No copie HeapMemory ni BoundedMemory después del primer uso.
heap := &takt.HeapMemory{}
loopA := takt.NewLoop[*myBackend, int](backendA, 128, takt.WithMemory(heap))
defer loopA.Drain()
loopB := takt.NewLoop[*myBackend, int](backendB, 128, takt.WithMemory(heap))
defer loopB.Drain()

// BoundedMemory: un pool acotado de slabs de 128 KiB con tamaño por defecto. WithPoolCapacity ajusta la capacidad de ese pool (iobuf la redondea a la siguiente potencia de dos).
bounded := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
boundedLoop := takt.NewLoop[*myBackend, int](
	backend, 128,
	takt.WithMemory(bounded),
	takt.WithMaxCompletions(64),
)
defer boundedLoop.Drain()
```

`BoundedMemory` limita el almacenamiento retenido en el pool, no toda la memoria en uso. Si el pool se agota, se asigna un slab adicional del tamaño predeterminado; las solicitudes mayores asignan slabs del tamaño exacto. `Release` no retiene esos slabs adicionales. Llame a `Drain` en cada bucle para devolver su almacenamiento, incluso tras completar el trabajo o fallar; la liberación de recursos nativos sigue siendo responsabilidad de la integración.

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
- `Completion`: token, resultado y carga de respuesta directa o inline
- `Completion.ResumeValue() kont.Resumed`: obtiene la respuesta de cualquiera de las dos representaciones
- `CompletionDecoder`: decodifica dos palabras sin punteros al reanudar
- `NewInlineCompletion(token Token, word0, word1 uint64, decoder CompletionDecoder, err error) Completion`: construye una finalización inline
- `NewLoop[B, R](b B, capacity int, opts ...Option) *Loop[B, R]`: crea un bucle de eventos (`HeapMemory` y slab predeterminado por defecto)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)`: reserva y evalúa; resultado inmediato o suspensión en cola
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)`: reserva y reifica; resultado inmediato o suspensión en cola
- `(*Loop[B, R]).Poll() ([]R, error)`: sondea y despacha finalizaciones
- `(*Loop[B, R]).Run() ([]R, error)`: conduce todo hasta completar
- `(*Loop[B, R]).SubmitSuspension(s *kont.Suspension[R]) error`
- `(*Loop[B, R]).Ready() bool`
- `(*Loop[B, R]).Pending() int`: linajes de computación locales, incluidos reintentos diferidos
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
- `Subscription[A]`: identidad opaca de ruta de stream
- `Subscription[A].ID() RouteID`: identidad de ruta transportada por el descriptor
- `Subscription[A].IsZero() bool`: prueba de descriptor cero; no consulta si la ruta sigue activa
- `StreamCompletion[A]`: `{ID, Value, HasValue, EventErr, More}`
- `StreamCompletion[A].RouteOutcome() iox.Outcome`: proyección al vocabulario de resultados `iox`
- `StreamEvent[A]`: `{Subscription, Value, HasValue, Final, EventErr}`
- `SubscriptionBackend[B, A]`: interfaz abstracta de backend de stream (`Subscribe`, `Poll`, `Cancel`)
- `SubscriptionOption`: opciones funcionales para `NewSubscriptionLoop`
- `WithMaxStreamCompletions(n)`: acota el búfer de finalizaciones de stream por sondeo
- `NewSubscriptionLoop[B, A](b B, capacity int, opts ...SubscriptionOption) *SubscriptionLoop[B, A]`: crea un ejecutor de stream
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

Use un `Backend` con `Loop`, o un `Dispatcher` con `Exec` y `Advance`. Son rutas separadas: Loop no llama automáticamente a Dispatcher. `ExecError` y `AdvanceError` interpretan explícitamente los efectos de error de aplicación. Un Loop sin esas capas necesita un backend/intérprete que maneje todas las operaciones admitidas, incluidos los efectos de error. Los [ejemplos ejecutables](example_test.go) muestran sucesoras, resultados inmediatos y admisión diferida al backend.

La guía de [verificación](docs/verification.md) incluye las comprobaciones y el alcance de las pruebas formales. La guía de [rendimiento](docs/performance.md) explica la localidad, la planificación, la espera adaptativa, el despacho tipado y las mediciones repetibles.

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
