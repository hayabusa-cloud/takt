[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | **Español** | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

Motor de despacho abstracto, guiado por eventos de completación, para pilas de E/S no bloqueantes.

## Descripción general

En un modelo proactor, las operaciones de E/S se envían al kernel y sus completaciones llegan de forma asíncrona. La
aplicación debe correlacionar cada completación con la computación que la solicitó, reanudar esa computación y manejar
todo el rango de resultados —éxito, progreso parcial, contrapresión y fallo.

`takt` proporciona este modelo de ejecución como una capa abstracta sobre el sistema de
efectos [kont](https://code.hybscloud.com/kont). Un `Dispatcher` evalúa un efecto algebraico a la vez y clasifica el
resultado según el álgebra de resultados [iox](https://code.hybscloud.com/iox). Un `Backend` envía operaciones a un
motor asíncrono (por ejemplo, `io_uring`) y sondea las completaciones. `Loop` los integra: envía computaciones, sondea
el backend, correlaciona completaciones por token y reanuda las continuaciones suspendidas.

Hay dos API equivalentes: `kont.Eff` (basada en clausuras y fácil de componer) y `kont.Expr` (basada en marcos, con
menor sobrecarga de asignación en rutas críticas).

La ruta del bucle de eventos guarda una sola suspensión pendiente por cada token vivo. Cada token sigue una suspensión
producida por `kont.StepExpr` (o por reificar antes `kont.Eff`), por lo que `Backend.Submit` no debe reutilizar un token
mientras la sumisión anterior que lo porta siga viva en el bucle.

## Instalación

```bash
go get code.hybscloud.com/takt
```

Requiere Go 1.26+.

## Clasificación de resultados

Cada operación despachada devuelve un resultado `iox`. El dispatcher y la API de stepping manejan cada caso:

| Resultado       | Significado                      | Dispatcher | API de stepping                    |
|-----------------|----------------------------------|------------|------------------------------------|
| `nil`           | completado                       | reanuda    | reanuda, devuelve `nil`            |
| `ErrMore`       | progreso, se espera continuación | reanuda    | reanuda, devuelve `ErrMore`        |
| `ErrWouldBlock` | sin progreso                     | espera     | devuelve la suspensión al llamador |
| otro            | fallo de infraestructura         | panic      | devuelve el error al llamador      |

## Uso

### Dispatcher

Un `Dispatcher` mapea cada efecto algebraico a una operación de E/S concreta y devuelve el resultado junto con su estado
`iox`.

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
// despachar op; devolver (value, nil) o (nil, iox.ErrWouldBlock)
}
```

### Evaluación bloqueante

`Exec` y `ExecExpr` ejecutan una computación hasta completarla, esperando de forma síncrona cuando el dispatcher
devuelve `iox.ErrWouldBlock`.

```go
result := takt.Exec(d, computation) // kont.Eff
result := takt.ExecExpr(d, exprComputation) // kont.Expr
```

### Paso a paso

Para bucles de eventos proactor (p. ej. `io_uring`), `Step` y `Advance` evalúan un efecto a la vez. Cuando el dispatcher
devuelve `iox.ErrWouldBlock`, la suspensión se devuelve al llamador, permitiendo al bucle de eventos reprogramar.

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

Componga operaciones del dispatcher con efectos de error. `Throw` cortocircuita la computación de forma inmediata y
descarta la suspensión pendiente.

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

Un `Loop` conduce computaciones a través de un `Backend`. Envía operaciones, sondea completaciones, las correlaciona por
`Token` y reanuda las continuaciones suspendidas. `NewLoop` acepta `Option` funcionales. `WithMaxCompletions(n)` entra
en pánico si `n <= 0` con `takt: WithMaxCompletions requires n > 0`; `WithMemory(nil)` entra en pánico con
`takt: WithMemory requires a non-nil CompletionMemory`.

`Backend.Poll([]Completion) (int, error)` informa tanto el número de completaciones listas como cualquier fallo de
infraestructura del sondeo. La `Loop` trata un `iox.ErrWouldBlock` devuelto por `Poll` como un ciclo en vacío y no como
un error terminal.

`Backend.Submit` debe devolver un token que sea único entre todas las sumisiones que sigan vivas en el bucle. Si un
backend reutiliza un token vivo, el bucle registra `ErrLiveTokenReuse`, descarta cada suspensión pendiente exactamente
una vez y toda llamada posterior a `SubmitExpr` / `Submit` / `Poll` / `Run` devuelve ese error fatal.

Cuando una completación trae `iox.ErrWouldBlock`, el bucle reenvía la misma operación. Si una completación
`iox.ErrMore` (multishot) tuviera que reanudarse en un nuevo efecto suspendido, el bucle registra
`ErrUnsupportedMultishot`, descarta cada suspensión pendiente exactamente una vez y toda llamada posterior a
`SubmitExpr` / `Submit` / `Poll` / `Run` devuelve ese error fatal. Ese rechazo preserva la correlación token-suspensión
del bucle: un linaje multishot puede seguir reanudando la suspensión actual, pero no crear un nuevo efecto pendiente
bajo la sumisión anterior.

`Loop.Failed()` reporta el error fatal registrado (o `nil`). `Loop.Drain()` fuerza al bucle al estado desechado,
descarta toda suspensión pendiente exactamente una vez y registra `ErrDisposed` solo si antes no había un error fatal;
es idempotente y preserva el fatal original cuando ya existe.

```go
loop := takt.NewLoop[*myBackend, int](backend, takt.WithMaxCompletions(64))

// Enviar computaciones
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // kont.Eff

// Conducir todo hasta completar
results, err := loop.Run()
```

`NewLoop` usa [`HeapMemory`](completion_memory.go) como proveedor predeterminado del búfer de completaciones. Si quiere
que esos búferes provengan de un pool acotado y estable de slabs de 128 KiB de tamaño predeterminado, pase [
`BoundedMemory`](completion_memory.go) mediante [`WithMemory`](option.go); o proporcione cualquier implementación de
`CompletionMemory` para controlar la estrategia de asignación sin ampliar los contratos de `Backend` ni de `Completion`.
Los proveedores personalizados deben devolver slabs vivos exclusivos y no solapados, y pueden tratar `Release` como una
transferencia de propiedad de vuelta al proveedor:

```go
// Por defecto: HeapMemory + slab de completación de tamaño por defecto.
loop := takt.NewLoop[*myBackend, int](backend)

// Limita la longitud del slab de completación por sondeo (Memory sigue eligiendo la forma del slab; Loop recorta la longitud visible a este tope).
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

- `Dispatcher[D Dispatcher[D]]` — interfaz de despacho no bloqueante
- `Exec[D, R](d D, m kont.Eff[R]) R` — evaluación bloqueante de `kont.Eff`
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — evaluación bloqueante de `kont.Expr`

### Paso a paso

- `SuspensionLike[S, R]` — interfaz reanudable con `Op` y `Resume`, implementada por `cove.SuspensionView`
- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — evalúa hasta la primera suspensión
- `AdvanceSuspension[D, S, R](d D, susp S) (R, S, error)` — despacha una operación a través de cualquier valor
  `SuspensionLike`
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — despacha una operación

### Manejo de errores

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — bloqueante con errores
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — evaluación de `kont.Expr` con manejo de errores
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — paso a paso con errores
-
`AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` —
avance con errores

### Backend y bucle de eventos

- `Backend[B Backend[B]]` — interfaz asíncrona de submit/poll
- `CompletionMemory` — proveedor local del búfer de completaciones del bucle
- `HeapMemory` — implementación predeterminada (`sync.Pool` con slabs tipados de tamaño predeterminado)
- `BoundedMemory` — implementación basada en iobuf con un único pool acotado de slabs de 128 KiB de tamaño
  predeterminado
- `Option` — opción funcional para `NewLoop` (`WithMemory`, `WithMaxCompletions`)
- `CompletionBufOption` — opción funcional para `CompletionMemory.CompletionBuf` (`WithSize`)
- `BoundedMemoryOption` — opción funcional para `NewBoundedMemory` (`WithPoolCapacity`)
- `Token` — correlación envío-completación (`uint64`)
- `Completion` — `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, opts ...Option) *Loop[B, R]` — crea un bucle de eventos (`HeapMemory` y slab predeterminado por
  defecto)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)` — avanza un paso y envía un Expr
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` — avanza un paso y envía un `kont.Eff`
- `(*Loop[B, R]).Poll() ([]R, error)` — sondea y despacha completaciones
- `(*Loop[B, R]).Run() ([]R, error)` — conduce todo hasta completar
- `(*Loop[B, R]).Pending() int` — número de operaciones pendientes
- `(*Loop[B, R]).Failed() error` — error fatal terminal, o nil
- `(*Loop[B, R]).Drain() int` — descarta suspensiones pendientes y desecha el bucle
- `ErrLiveTokenReuse` — el backend reutilizó un token que seguía vivo en el bucle
- `ErrUnsupportedMultishot` — una completación multishot no puede suspenderse en un nuevo efecto
- `ErrDisposed` — el bucle ha sido desechado mediante `Drain`

### Puente

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Cont

## Patrones prácticos

Una integración completa con un bucle de eventos combina un `Dispatcher` (la semántica síncrona) con un `Backend` (el
proactor) bajo una misma `Loop`:

```go
// 1. Definir el dispatcher: mapea una operación de efecto a un resultado iox.
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // Devolver (value, nil) al completar o (nil, iox.ErrWouldBlock) para ceder.
}

// 2. Definir el backend: envía operaciones al proactor del SO y sondea completaciones.
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

Para composición con manejo de errores, use `ExecError` / `StepError` / `AdvanceError` en lugar de sus equivalentes sin
error; `Throw` cortocircuita la computación en curso sin afectar a las computaciones hermanas del mismo bucle. El patrón
fusionado dispatcher+backend mostrado aquí es precisamente el que usa `sess` para conectar un endpoint de sesión a un
runtime de E/S real.

## Referencias

- Tarmo Uustalu and Varmo Vene. 2008. Comonadic Notions of Computation. *Electronic Notes in Theoretical Computer
  Science* 203, 5 (June 2008), 263–284. https://doi.org/10.1016/j.entcs.2008.05.029
- Gordon D. Plotkin and Matija Pretnar. 2009. Handlers of Algebraic Effects. In *Proc. 18th European Symposium on
  Programming (ESOP '09)*. LNCS 5502, 80–94. https://doi.org/10.1007/978-3-642-00590-9_7
- Andrej Bauer and Matija Pretnar. 2015. Programming with Algebraic Effects and Handlers. *Journal of Logical and
  Algebraic Methods in Programming* 84, 1 (Jan. 2015), 108–123. https://arxiv.org/abs/1203.1539
- Daniel Leijen. 2017. Type Directed Compilation of Row-Typed Algebraic Effects. In *Proc. 44th ACM SIGPLAN Symposium on
  Principles of Programming Languages (POPL '17)*. 486–499. https://doi.org/10.1145/3009837.3009872
- Danel Ahman and Andrej Bauer. 2020. Runners in Action. In *Proc. 29th European Symposium on Programming (ESOP '20)*.
  LNCS 12075, 29–55. https://arxiv.org/abs/1910.11629
- Daniel Hillerström, Sam Lindley, and Robert Atkey. 2020. Effect Handlers via Generalised Continuations. *Journal of
  Functional Programming* 30 (2020), e5. https://bentnib.org/handlers-cps-journal.pdf

## Licencia

Licencia MIT. Consulte [LICENSE](LICENSE) para más detalles.

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
