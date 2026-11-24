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

// This fixed provider owns scalar observations, not simulated kernel resources.
type eventBackend struct {
	ready     [64]takt.StreamCompletion[*int]
	n         int
	next      uint64
	pollError error
}

func (b *eventBackend) Subscribe(kont.Operation) (takt.RouteID, error) {
	b.next++
	return takt.NewRouteID(collisionToken(b.next), 0), nil
}

func (b *eventBackend) Poll(out []takt.StreamCompletion[*int]) (int, error) {
	if b.pollError != nil {
		return 0, b.pollError
	}
	n := copy(out, b.ready[:b.n])
	copy(b.ready[:], b.ready[n:b.n])
	clear(b.ready[b.n-n : b.n])
	b.n -= n
	return n, nil
}

func (*eventBackend) Cancel(takt.RouteID) error { return nil }

func TestSubscriptionClearsExpiredEvents(t *testing.T) {
	for _, boundary := range []string{"shorter", "idle", "would-block", "failure", "drain"} {
		t.Run(boundary, func(t *testing.T) {
			b := &eventBackend{}
			l := takt.NewSubscriptionLoop[*eventBackend, *int](b, 1, takt.WithMaxStreamCompletions(3))
			defer l.Drain()
			s, err := l.Subscribe(echoOp{})
			if err != nil {
				t.Fatal(err)
			}
			values := [3]int{1, 2, 3}
			payloadErr := errors.New("payload")
			for i := range values {
				b.ready[i] = takt.StreamCompletion[*int]{ID: s.ID(), Value: &values[i], HasValue: true, EventErr: payloadErr, More: true}
			}
			b.n = len(values)
			borrowed, err := l.Poll()
			if err != nil || len(borrowed) != len(values) {
				t.Fatalf("first batch: %v, %v", borrowed, err)
			}
			saved := append([]takt.StreamEvent[*int](nil), borrowed...)
			start := 0
			switch boundary {
			case "shorter":
				b.ready[0] = takt.StreamCompletion[*int]{ID: s.ID(), More: true}
				b.n = 1
				start = 1
			case "would-block":
				b.pollError = iox.ErrWouldBlock
			case "failure":
				b.pollError = errors.New("service")
			}
			if boundary == "drain" {
				l.Drain()
			} else {
				events, err := l.Poll()
				if boundary == "failure" {
					if err != b.pollError {
						t.Fatal(err)
					}
				} else if err != nil || len(events) != start {
					t.Fatalf("next batch: %v, %v", events, err)
				}
			}
			// Intentional storage probe after expiration, not a supported caller use.
			for _, ev := range borrowed[start:] {
				if ev != (takt.StreamEvent[*int]{}) {
					t.Fatalf("expired event retained: %+v", ev)
				}
			}
			for i, ev := range saved {
				if ev.Value != &values[i] || ev.EventErr != payloadErr {
					t.Fatal("clearing a borrowed view changed a caller-owned copy")
				}
			}
		})
	}
}

func TestFirstUseSubscriptionBookkeepingAllocations(t *testing.T) {
	const runs = 16
	var loops [runs + 1]*takt.SubscriptionLoop[*eventBackend, *int]
	var backends [runs + 1]eventBackend
	var op kont.Operation = echoOp{}
	value := 7
	for i := range loops {
		loops[i] = takt.NewSubscriptionLoop[*eventBackend, *int](&backends[i], 64, takt.WithMaxStreamCompletions(64))
		defer loops[i].Drain()
	}
	i := 0
	allocs := testing.AllocsPerRun(runs, func() {
		l, b := loops[i], &backends[i]
		for range 2 { // fresh admission, then reuse with expired pointer-bearing events
			for j := range b.ready {
				s, err := l.Subscribe(op)
				if err != nil {
					panic(err)
				}
				b.ready[j] = takt.StreamCompletion[*int]{ID: s.ID(), Value: &value, HasValue: true}
			}
			b.n = len(b.ready)
			if events, err := l.Poll(); err != nil || len(events) != len(b.ready) || l.Pending() != 0 {
				panic("lost colliding route")
			}
		}
		i++
	})
	if allocs != 0 {
		t.Fatalf("fresh provisioned stream loop: %g allocations", allocs)
	}
}
