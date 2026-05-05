// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"errors"
	"testing"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

type streamBackend struct {
	ids          []takt.RouteID
	nextToken    takt.Token
	ready        []takt.StreamCompletion[int]
	subscribeErr error
	pollErr      error
	cancelErr    error
	cancelled    []takt.RouteID
	seenBufLen   int
}

func (b *streamBackend) Subscribe(op kont.Operation) (takt.RouteID, error) {
	if b.subscribeErr != nil {
		return takt.RouteID{}, b.subscribeErr
	}
	if len(b.ids) > 0 {
		id := b.ids[0]
		b.ids = b.ids[1:]
		return id, nil
	}
	b.nextToken++
	return takt.NewRouteID(b.nextToken, 1), nil
}

func (b *streamBackend) Poll(completions []takt.StreamCompletion[int]) (int, error) {
	b.seenBufLen = len(completions)
	if b.pollErr != nil {
		return 0, b.pollErr
	}
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

func (b *streamBackend) Cancel(id takt.RouteID) error {
	if b.cancelErr != nil {
		return b.cancelErr
	}
	b.cancelled = append(b.cancelled, id)
	return nil
}

func TestRouteIDAccessors(t *testing.T) {
	id := takt.NewRouteID(7, 3)
	if id.IsZero() {
		t.Fatal("non-zero route reported as zero")
	}
	if id.Token() != 7 {
		t.Fatalf("Token()=%d want 7", id.Token())
	}
	if id.Generation() != 3 {
		t.Fatalf("Generation()=%d want 3", id.Generation())
	}
	if !(takt.RouteID{}).IsZero() {
		t.Fatal("zero RouteID did not report IsZero")
	}
}

func TestSubscriptionLoopLiveValueBoundaryKeepsRoute(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	if sub.IsZero() {
		t.Fatal("Subscribe returned zero subscription")
	}
	b.ready = append(b.ready, takt.StreamCompletion[int]{
		ID:       sub.ID(),
		Value:    11,
		HasValue: true,
		More:     true,
	})

	events, err := l.Poll()
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	ev := events[0]
	if ev.Subscription.ID() != sub.ID() {
		t.Fatalf("event subscription=%v want %v", ev.Subscription.ID(), sub.ID())
	}
	if ev.Final {
		t.Fatal("live More boundary reported final")
	}
	if !ev.HasValue || ev.Value != 11 {
		t.Fatalf("event value=(%v,%d), want (true,11)", ev.HasValue, ev.Value)
	}
	if l.Pending() != 1 {
		t.Fatalf("pending=%d want 1", l.Pending())
	}
}

func TestSubscriptionLoopLiveEmptyBoundaryKeepsRoute(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	b.ready = append(b.ready, takt.StreamCompletion[int]{
		ID:   sub.ID(),
		More: true,
	})

	events, err := l.Poll()
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	if events[0].HasValue {
		t.Fatal("empty live boundary reported HasValue")
	}
	if events[0].Final {
		t.Fatal("empty live boundary reported final")
	}
	if l.Pending() != 1 {
		t.Fatalf("pending=%d want 1", l.Pending())
	}
}

func TestSubscriptionLoopLivePayloadErrorKeepsRoute(t *testing.T) {
	payloadErr := errors.New("payload error")
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	completion := takt.StreamCompletion[int]{
		ID:       sub.ID(),
		EventErr: payloadErr,
		More:     true,
	}
	if got := completion.RouteOutcome(); got != iox.OutcomeMore {
		t.Fatalf("RouteOutcome=%v want OutcomeMore", got)
	}
	b.ready = append(b.ready, completion)

	events, err := l.Poll()
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	if events[0].EventErr != payloadErr {
		t.Fatalf("EventErr=%v want %v", events[0].EventErr, payloadErr)
	}
	if events[0].Final {
		t.Fatal("payload-error live boundary reported final")
	}
	if l.Pending() != 1 {
		t.Fatalf("pending=%d want 1", l.Pending())
	}
}

func TestStreamCompletionRouteOutcomeOK(t *testing.T) {
	completion := takt.StreamCompletion[int]{
		ID:       takt.NewRouteID(3, 1),
		Value:    17,
		HasValue: true,
	}
	if got := completion.RouteOutcome(); got != iox.OutcomeOK {
		t.Fatalf("RouteOutcome=%v want OutcomeOK", got)
	}
}

func TestSubscriptionLoopTerminalSuccessRetiresRoute(t *testing.T) {
	id1 := takt.NewRouteID(9, 1)
	id2 := takt.NewRouteID(9, 2)
	b := &streamBackend{ids: []takt.RouteID{id1, id2}}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	b.ready = append(b.ready, takt.StreamCompletion[int]{
		ID:       sub.ID(),
		Value:    15,
		HasValue: true,
	})

	events, err := l.Poll()
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	if !events[0].Final || !events[0].HasValue || events[0].Value != 15 {
		t.Fatalf("event=%+v want final value 15", events[0])
	}
	if l.Pending() != 0 {
		t.Fatalf("pending=%d want 0", l.Pending())
	}

	reused, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe after final error: %v", err)
	}
	if reused.ID() != id2 {
		t.Fatalf("reused ID=%v want %v", reused.ID(), id2)
	}
}

func TestSubscriptionLoopTerminalFailureRetiresRoute(t *testing.T) {
	payloadErr := errors.New("terminal payload error")
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	completion := takt.StreamCompletion[int]{ID: sub.ID(), EventErr: payloadErr}
	if got := completion.RouteOutcome(); got != iox.OutcomeFailure {
		t.Fatalf("RouteOutcome=%v want OutcomeFailure", got)
	}
	b.ready = append(b.ready, completion)

	events, err := l.Poll()
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	if !events[0].Final {
		t.Fatal("terminal failure did not report final")
	}
	if events[0].EventErr != payloadErr {
		t.Fatalf("EventErr=%v want %v", events[0].EventErr, payloadErr)
	}
	if l.Pending() != 0 {
		t.Fatalf("pending=%d want 0", l.Pending())
	}
	if err := l.Cancel(sub); !errors.Is(err, takt.ErrUnknownSubscription) {
		t.Fatalf("Cancel after final=%v want ErrUnknownSubscription", err)
	}
}

func TestSubscriptionLoopSubscribeFailureDoesNotPoisonLoop(t *testing.T) {
	subscribeErr := errors.New("subscribe failed")
	b := &streamBackend{subscribeErr: subscribeErr}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))

	sub, err := l.Subscribe(echoOp{})
	if err != subscribeErr {
		t.Fatalf("Subscribe error=%v want %v", err, subscribeErr)
	}
	if !sub.IsZero() {
		t.Fatalf("subscription=%v want zero", sub.ID())
	}
	if l.Pending() != 0 {
		t.Fatalf("pending=%d want 0", l.Pending())
	}
	if l.Failed() != nil {
		t.Fatalf("Failed=%v want nil", l.Failed())
	}

	b.subscribeErr = nil
	recovered, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe after backend recovery error: %v", err)
	}
	if recovered.IsZero() {
		t.Fatal("Subscribe after backend recovery returned zero subscription")
	}
	if l.Pending() != 1 {
		t.Fatalf("pending=%d want 1", l.Pending())
	}
}

func TestSubscriptionLoopPollWouldBlockIsIdle(t *testing.T) {
	b := &streamBackend{pollErr: iox.ErrWouldBlock}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	if _, err := l.Subscribe(echoOp{}); err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	events, err := l.Poll()
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if events != nil {
		t.Fatalf("events=%#v want nil", events)
	}
	if l.Pending() != 1 {
		t.Fatalf("pending=%d want 1", l.Pending())
	}
}

func TestSubscriptionLoopZeroRouteCompletionIgnored(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	if _, err := l.Subscribe(echoOp{}); err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	b.ready = append(b.ready, takt.StreamCompletion[int]{
		ID:       takt.RouteID{},
		Value:    33,
		HasValue: true,
		More:     true,
	})

	events, err := l.Poll()
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if events != nil {
		t.Fatalf("events=%#v want nil", events)
	}
	if l.Pending() != 1 {
		t.Fatalf("pending=%d want 1", l.Pending())
	}
}

func TestSubscriptionLoopCancelPreservesRouteUntilTerminal(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	if err := l.Cancel(sub); err != nil {
		t.Fatalf("Cancel error: %v", err)
	}
	if err := l.Cancel(sub); err != nil {
		t.Fatalf("second Cancel error: %v", err)
	}
	if len(b.cancelled) != 1 {
		t.Fatalf("cancel calls=%d want 1", len(b.cancelled))
	}
	if l.Pending() != 1 {
		t.Fatalf("pending after cancel=%d want 1", l.Pending())
	}

	b.ready = append(b.ready, takt.StreamCompletion[int]{ID: sub.ID()})
	events, err := l.Poll()
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if len(events) != 1 || !events[0].Final {
		t.Fatalf("events=%+v want one final event", events)
	}
	if l.Pending() != 0 {
		t.Fatalf("pending=%d want 0", l.Pending())
	}
}

func TestSubscriptionLoopCancelZeroSubscription(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))

	err := l.Cancel(takt.Subscription[int]{})
	if !errors.Is(err, takt.ErrUnknownSubscription) {
		t.Fatalf("Cancel zero subscription=%v want ErrUnknownSubscription", err)
	}
	if len(b.cancelled) != 0 {
		t.Fatalf("cancel calls=%d want 0", len(b.cancelled))
	}
	if l.Failed() != nil {
		t.Fatalf("Failed=%v want nil", l.Failed())
	}
}

func TestSubscriptionLoopCancelFailurePreservesActiveRoute(t *testing.T) {
	cancelErr := errors.New("cancel failed")
	b := &streamBackend{cancelErr: cancelErr}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	if err := l.Cancel(sub); err != cancelErr {
		t.Fatalf("Cancel error=%v want %v", err, cancelErr)
	}
	if l.Pending() != 1 {
		t.Fatalf("pending=%d want 1", l.Pending())
	}
}

func TestSubscriptionLoopDrainCancelsActiveRoutesOnce(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub1, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe 1 error: %v", err)
	}
	if _, err := l.Subscribe(echoOp{}); err != nil {
		t.Fatalf("Subscribe 2 error: %v", err)
	}
	if err := l.Cancel(sub1); err != nil {
		t.Fatalf("Cancel error: %v", err)
	}

	if drained := l.Drain(); drained != 2 {
		t.Fatalf("Drain=%d want 2", drained)
	}
	if len(b.cancelled) != 2 {
		t.Fatalf("cancel calls=%d want 2", len(b.cancelled))
	}
	if l.Pending() != 0 {
		t.Fatalf("pending=%d want 0", l.Pending())
	}
	if err := l.Failed(); !errors.Is(err, takt.ErrDisposed) {
		t.Fatalf("Failed=%v want ErrDisposed", err)
	}
	if drained := l.Drain(); drained != 0 {
		t.Fatalf("second Drain=%d want 0", drained)
	}
	if len(b.cancelled) != 2 {
		t.Fatalf("second Drain cancel calls=%d want 2", len(b.cancelled))
	}
}

func TestSubscriptionLoopRejectsLiveRouteReuse(t *testing.T) {
	id := takt.NewRouteID(1, 1)
	b := &streamBackend{ids: []takt.RouteID{id, id}}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	if _, err := l.Subscribe(echoOp{}); err != nil {
		t.Fatalf("first Subscribe error: %v", err)
	}
	if _, err := l.Subscribe(echoOp{}); !errors.Is(err, takt.ErrLiveRouteReuse) {
		t.Fatalf("second Subscribe=%v want ErrLiveRouteReuse", err)
	}
	if l.Pending() != 0 {
		t.Fatalf("pending=%d want 0", l.Pending())
	}
	if err := l.Failed(); !errors.Is(err, takt.ErrLiveRouteReuse) {
		t.Fatalf("Failed=%v want ErrLiveRouteReuse", err)
	}
	if len(b.cancelled) != 1 || b.cancelled[0] != id {
		t.Fatalf("cancelled=%v want [%v]", b.cancelled, id)
	}
}

func TestSubscriptionLoopRejectsInvalidRouteID(t *testing.T) {
	b := &streamBackend{ids: []takt.RouteID{{}}}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	if _, err := l.Subscribe(echoOp{}); !errors.Is(err, takt.ErrInvalidRouteID) {
		t.Fatalf("Subscribe=%v want ErrInvalidRouteID", err)
	}
	if err := l.Failed(); !errors.Is(err, takt.ErrInvalidRouteID) {
		t.Fatalf("Failed=%v want ErrInvalidRouteID", err)
	}
}

func TestSubscriptionLoopStaleCompletionIgnored(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	b.ready = append(b.ready, takt.StreamCompletion[int]{ID: sub.ID()})
	if _, err := l.Poll(); err != nil {
		t.Fatalf("terminal Poll error: %v", err)
	}

	b.ready = append(b.ready, takt.StreamCompletion[int]{
		ID:       sub.ID(),
		Value:    99,
		HasValue: true,
		More:     true,
	})
	events, err := l.Poll()
	if err != nil {
		t.Fatalf("stale Poll error: %v", err)
	}
	if events != nil {
		t.Fatalf("stale events=%#v want nil", events)
	}
}

func TestSubscriptionLoopPollFailurePoisonsAndCancels(t *testing.T) {
	pollErr := errors.New("poll failed")
	b := &streamBackend{pollErr: pollErr}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	sub, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}
	events, err := l.Poll()
	if err != pollErr {
		t.Fatalf("Poll error=%v want %v", err, pollErr)
	}
	if events != nil {
		t.Fatalf("events=%#v want nil", events)
	}
	if l.Pending() != 0 {
		t.Fatalf("pending=%d want 0", l.Pending())
	}
	if l.Failed() != pollErr {
		t.Fatalf("Failed=%v want %v", l.Failed(), pollErr)
	}
	if len(b.cancelled) != 1 || b.cancelled[0] != sub.ID() {
		t.Fatalf("cancelled=%v want [%v]", b.cancelled, sub.ID())
	}
}

func TestSubscriptionLoopFatalMethodsReturnStoredError(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(4))
	l.Drain()
	if _, err := l.Subscribe(echoOp{}); !errors.Is(err, takt.ErrDisposed) {
		t.Fatalf("Subscribe after Drain=%v want ErrDisposed", err)
	}
	if _, err := l.Poll(); !errors.Is(err, takt.ErrDisposed) {
		t.Fatalf("Poll after Drain=%v want ErrDisposed", err)
	}
	if err := l.Cancel(takt.Subscription[int]{}); !errors.Is(err, takt.ErrDisposed) {
		t.Fatalf("Cancel after Drain=%v want ErrDisposed", err)
	}
}

func TestSubscriptionLoopRoutesMaxStreamCompletions(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, takt.WithMaxStreamCompletions(3))
	if _, err := l.Poll(); err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if b.seenBufLen != 3 {
		t.Fatalf("Poll buffer len=%d want 3", b.seenBufLen)
	}
}

func TestWithMaxStreamCompletionsRejectsNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		func(n int) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("WithMaxStreamCompletions(%d): expected panic", n)
				}
			}()
			_ = takt.WithMaxStreamCompletions(n)
		}(n)
	}
}
