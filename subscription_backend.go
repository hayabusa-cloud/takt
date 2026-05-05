// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
)

// RouteID identifies one stream/subscription route.
// It pairs a backend token with an explicit generation so a backend may reuse
// a token after finalization without aliasing a previous live route.
type RouteID struct {
	token      Token
	generation uint64
}

// NewRouteID constructs a route identifier for [SubscriptionBackend]
// implementations. The zero [RouteID] is reserved and must not be returned by
// a backend.
func NewRouteID(token Token, generation uint64) RouteID {
	return RouteID{token: token, generation: generation}
}

// Token returns the token component of id.
func (id RouteID) Token() Token {
	return id.token
}

// Generation returns the generation component of id.
func (id RouteID) Generation() uint64 {
	return id.generation
}

// IsZero reports whether id is the reserved invalid route.
func (id RouteID) IsZero() bool {
	return id == RouteID{}
}

// Subscription is an opaque handle for one abstract stream route.
// It carries route identity only; payload delivery and finalization are
// observed through [SubscriptionLoop.Poll].
type Subscription[A any] struct {
	id RouteID
}

// ID returns the route identifier associated with the subscription.
func (s Subscription[A]) ID() RouteID {
	return s.id
}

// IsZero reports whether s is the zero, non-live subscription handle.
func (s Subscription[A]) IsZero() bool {
	return s.id.IsZero()
}

// StreamCompletion carries backend evidence for one stream route.
//
// More is route-liveness evidence: when true, the same route remains active
// after this observation. HasValue and EventErr describe payload evidence at
// this boundary and are independent of More. EventErr is payload evidence, not
// route control; use More to report the live frontier.
type StreamCompletion[A any] struct {
	ID       RouteID
	Value    A
	HasValue bool
	EventErr error
	More     bool
}

// RouteOutcome projects the route-level completion onto iox's endpoint outcome
// vocabulary.
// The projection forgets route identity and successor ownership: More maps to
// [iox.OutcomeMore], EventErr maps to [iox.OutcomeFailure], and all other
// observations map to [iox.OutcomeOK].
func (c StreamCompletion[A]) RouteOutcome() iox.Outcome {
	if c.More {
		return iox.OutcomeMore
	}
	if c.EventErr != nil {
		return iox.OutcomeFailure
	}
	return iox.OutcomeOK
}

// StreamEvent is the user-visible event emitted by [SubscriptionLoop.Poll].
// Final reports that the route has retired after this observation.
type StreamEvent[A any] struct {
	Subscription Subscription[A]
	Value        A
	HasValue     bool
	Final        bool
	EventErr     error
}

// SubscriptionBackend is the abstract backend contract for route-indexed
// stream/subscription execution.
type SubscriptionBackend[B SubscriptionBackend[B, A], A any] interface {
	// Subscribe starts op as a route-producing operation and returns its route
	// identity. Returned routes must be unique among routes still live in the
	// [SubscriptionLoop], and the zero [RouteID] is invalid.
	Subscribe(op kont.Operation) (RouteID, error)

	// Poll writes ready stream completions into completions. Poll-level errors
	// report failures of the polling operation itself; per-route payload errors
	// are reported in [StreamCompletion.EventErr]. A backend must not return
	// n > 0 and err != nil together; [SubscriptionLoop] handles poll errors
	// separately from route completions. A poll-level [iox.ErrWouldBlock] means
	// the stream loop is idle.
	Poll(completions []StreamCompletion[A]) (int, error)

	// Cancel requests cancellation of the live route. A successful Cancel moves
	// the route into a canceling state; the route is retired by a later terminal
	// completion, by Drain, or by a fatal loop transition.
	Cancel(id RouteID) error
}
