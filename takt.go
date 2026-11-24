// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"errors"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

// Dispatcher is the non-blocking execution boundary for one suspended
// operation.
// Dispatch returns a valid reply with nil or [iox.ErrMore]. The latter also
// reports a live interaction; it does not permit reusing the same continuation.
// [iox.ErrWouldBlock] and ordinary failures leave the suspension unconsumed;
// their returned value is not delivered. The dispatcher must retain any partial
// progress and operands needed to retry that same operation without replay.
// Ordinary operation errors can be encoded in valid replies; infrastructure
// failures remain separate. Control classification does not measure progress.
// Classification follows [iox.Classify], including wrapped errors. An error
// matching both semantic sentinels is WouldBlock and cannot authorize resumption.
type Dispatcher[D Dispatcher[D]] interface {
	Dispatch(op kont.Operation) (kont.Resumed, error)
}

// ErrLiveTokenReuse reports that a [Backend] reused a token that is still live
// in the current [Loop]. Reusing a live token would alias two pending
// frontiers under one correlation key and break token-to-suspension tracking.
var ErrLiveTokenReuse = errors.New("takt: backend reused a live token")

// ErrLiveRouteReuse reports that a [SubscriptionBackend] reused a route that
// is still live in the current [SubscriptionLoop].
var ErrLiveRouteReuse = errors.New("takt: backend reused a live route")

// ErrInvalidRouteID reports that a [SubscriptionBackend] returned the reserved
// zero [RouteID].
var ErrInvalidRouteID = errors.New("takt: backend returned an invalid route id")

// ErrUnknownSubscription reports that a [Subscription] is not live in the
// current [SubscriptionLoop].
var ErrUnknownSubscription = errors.New("takt: unknown subscription")

// ErrUnsupportedMultishot reports that a backend produced a multishot
// completion for [Loop]. [iox.ErrMore] means the submitted backend operation
// remains active after the CQE; generic Loop tracks affine one-shot
// suspensions and has no subscription or cancel carrier for later same-token
// completions.
var ErrUnsupportedMultishot = errors.New("takt: unsupported multishot completion")

// ErrDisposed reports that a loop has been explicitly disposed via [Loop.Drain]
// or [SubscriptionLoop.Drain] and can no longer accept submissions or produce
// completions.
var ErrDisposed = errors.New("takt: loop disposed")

// dispatchFailed panics on infrastructure failure.
// noinline keeps Dispatch methods inlineable.
//
//go:noinline
func dispatchFailed(err error) {
	panic("takt: dispatch failed: " + err.Error())
}

// handler adapts a Dispatcher as kont.Handler.
// It waits on ErrWouldBlock with adaptive backoff. Value type for stack allocation.
type handler[D Dispatcher[D], R any] struct {
	d D
}

// Dispatch delegates to dispatchWait.
func (h handler[D, R]) Dispatch(op kont.Operation) (kont.Resumed, bool) {
	return dispatchWait(h.d, op), true
}

// dispatchWait loops until Dispatch reports progress.
// WouldBlock waits with adaptive backoff; failures panic.
func dispatchWait[D Dispatcher[D]](d D, op kont.Operation) kont.Resumed {
	var bo iox.Backoff
	for {
		v, err := d.Dispatch(op)
		if err == nil {
			return v
		}
		switch iox.Classify(err) {
		case iox.OutcomeMore:
			return v
		case iox.OutcomeWouldBlock:
			bo.Wait()
		default:
			dispatchFailed(err)
		}
	}
}

// Exec runs a [kont.Eff] computation to completion via a Dispatcher.
func Exec[D Dispatcher[D], R any](d D, m kont.Eff[R]) R {
	return kont.Handle(m, handler[D, R]{d: d})
}

// ExecExpr runs a [kont.Expr] computation to completion via a Dispatcher.
func ExecExpr[D Dispatcher[D], R any](d D, m kont.Expr[R]) R {
	return kont.HandleExpr(m, handler[D, R]{d: d})
}
