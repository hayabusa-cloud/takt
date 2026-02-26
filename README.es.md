[![Go Reference](https://pkg.go.dev/badge/code.hybscloud.com/takt.svg)](https://pkg.go.dev/code.hybscloud.com/takt)
[![Go Report Card](https://goreportcard.com/badge/github.com/hayabusa-cloud/takt)](https://goreportcard.com/report/github.com/hayabusa-cloud/takt)
[![Coverage Status](https://codecov.io/gh/hayabusa-cloud/takt/graph/badge.svg)](https://codecov.io/gh/hayabusa-cloud/takt)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md) | **Español** | [日本語](README.ja.md) | [Français](README.fr.md)

# takt

Motor de despacho abstracto dirigido por eventos de completacion para pilas de E/S no bloqueante.

## Descripcion General

En un modelo proactor, las operaciones de E/S se envian al kernel y sus completaciones llegan de forma asincrona. La aplicacion debe correlacionar cada completacion con la computacion que la solicito, reanudar esa computacion y manejar el rango completo de resultados — exito, progreso parcial, contrapresion y fallo.

`takt` proporciona esta algebra de despacho como una capa abstracta sobre el sistema de efectos [kont](https://code.hybscloud.com/kont). Un `Dispatcher` evalua un efecto algebraico a la vez, clasificando el resultado segun la algebra de resultados [iox](https://code.hybscloud.com/iox). Un `Backend` envia operaciones a un motor asincrono (ej., `io_uring`) y sondea las completaciones. El bucle de eventos `Loop` los une: envia computaciones, sondea el backend, correlaciona completaciones por token y reanuda las continuaciones suspendidas.

Dos APIs equivalentes: Cont (basado en clausuras, composicion directa) y Expr (defuncionalizado, menor sobrecarga de asignaciones en rutas criticas).

## Instalacion

```bash
go get code.hybscloud.com/takt
```

Requires Go 1.26+.

## Clasificacion de Resultados

Cada operacion despachada retorna un resultado `iox`. El dispatcher y la API de stepping manejan cada caso:

| Resultado | Significado | Dispatcher | API de Stepping |
-----------|-------------|------------|-----------------|
 `nil` | completado | reanudar | reanudar, retornar `nil` |
 `ErrMore` | progreso, se espera mas | reanudar | reanudar, retornar `ErrMore` |
 `ErrWouldBlock` | sin progreso | esperar | retornar suspension al llamador |
 otro | fallo de infraestructura | panic | retornar error al llamador |

## Uso

### Dispatcher

Un `Dispatcher` mapea cada efecto algebraico a una operacion de E/S concreta y retorna el resultado con un resultado `iox`.

```go
type myDispatcher struct{ /* ... */ }

func (d *myDispatcher) Dispatch(op kont.Operation) (kont.Resumed, error) {
    // dispatch op, return (value, nil) or (nil, iox.ErrWouldBlock)
}
```

### Evaluacion Bloqueante

`Exec` y `ExecExpr` ejecutan una computacion hasta completarla, esperando sincronamente cuando el dispatcher devuelve `iox.ErrWouldBlock`.

```go
result := takt.Exec(d, computation)         // Cont-world
result := takt.ExecExpr(d, exprComputation) // Expr-world
```

### Stepping

Para bucles de eventos proactor (ej., `io_uring`), `Step` y `Advance` evaluan un efecto a la vez. Cuando el dispatcher devuelve `iox.ErrWouldBlock`, la suspension se retorna al llamador, permitiendo al bucle de eventos reprogramar.

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

### Manejo de Errores

Componga efectos de dispatcher con efectos de error. `Throw` cortocircuita la computacion y descarta la suspension pendiente.

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

### Bucle de Eventos

Un `Loop` impulsa computaciones a traves de un `Backend`. Envia operaciones, sondea completaciones, las correlaciona por `Token` y reanuda las continuaciones suspendidas.
`maxCompletions` en `NewLoop` debe ser mayor que 0.

```go
loop := takt.NewLoop[*myBackend, int](backend, 64)

// Submit computations
loop.SubmitExpr(exprComputation1)
loop.SubmitExpr(exprComputation2)
loop.Submit(contComputation) // Cont-world

// Drive all to completion
results, err := loop.Run()
```

## Resumen de API

### Despacho

- `Dispatcher[D Dispatcher[D]]` — F-bounded dispatch interface
- `Exec[D, R](d D, m kont.Eff[R]) R` — blocking Cont-world evaluation
- `ExecExpr[D, R](d D, m kont.Expr[R]) R` — blocking Expr-world evaluation

### Stepping

- `Step[R](m kont.Expr[R]) (R, *kont.Suspension[R])` — evaluate to first suspension
- `Advance[D, R](d D, susp *kont.Suspension[R]) (R, *kont.Suspension[R], error)` — dispatch one operation

### Manejo de Errores

- `ExecError[E, D, R](d D, m kont.Eff[R]) kont.Either[E, R]` — blocking with errors
- `ExecErrorExpr[E, D, R](d D, m kont.Expr[R]) kont.Either[E, R]` — Expr-world with errors
- `StepError[E, R](m kont.Expr[R]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]])` — step with errors
- `AdvanceError[E, D, R](d D, susp *kont.Suspension[kont.Either[E, R]]) (kont.Either[E, R], *kont.Suspension[kont.Either[E, R]], error)` — advance with errors

### Backend y Bucle de Eventos

- `Backend[B Backend[B]]` — F-bounded async submit/poll interface
- `Token` — submission-completion correlation (`uint64`)
- `Completion` — `{Token, Value kont.Resumed, Err error}`
- `NewLoop[B, R](b B, maxCompletions int) *Loop[B, R]` — create event loop (`maxCompletions > 0`)
- `(*Loop[B, R]).SubmitExpr(m kont.Expr[R]) (R, bool, error)` — step and submit Expr
- `(*Loop[B, R]).Submit(m kont.Eff[R]) (R, bool, error)` — step and submit Cont
- `(*Loop[B, R]).Poll() ([]R, error)` — poll and dispatch completions
- `(*Loop[B, R]).Run() ([]R, error)` — drive all to completion
- `(*Loop[B, R]).Pending() int` — count pending operations

### Puente

- `Reify[A](kont.Eff[A]) kont.Expr[A]` — Cont → Expr
- `Reflect[A](kont.Expr[A]) kont.Eff[A]` — Expr → Cont

## References

- G. D. Plotkin and M. Pretnar. "Handlers of Algebraic Effects." In *Proc. ESOP*, 2009.
- T. Uustalu and V. Vene. "Comonadic Notions of Computation." In *ENTCS* 203(5), 2008.
- D. Ahman and A. Bauer. "Runners in Action." In *Proc. ESOP*, 2020.

## Licencia

Licencia MIT. Ver [LICENSE](LICENSE) para detalles.

©2026 [Hayabusa Cloud Co., Ltd.](https://code.hybscloud.com)
