// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"math/bits"
	"math/rand/v2"
	"slices"
	"testing"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/takt"
)

type historyStream struct {
	streamBackend
	cancelCalls int
}

func (b *historyStream) Cancel(id takt.RouteID) error {
	b.cancelCalls++
	return b.streamBackend.Cancel(id)
}

func checkStreamHistory(t *testing.T, data []byte) {
	t.Helper()
	if len(data) > 1024 {
		data = data[:1024]
	}
	in := historyInput{data: data}
	capacity, budget := int(in.next()%16)+1, int(in.next()%8)+1
	b := &historyStream{}
	l := takt.NewSubscriptionLoop[*historyStream, int](b, capacity, takt.WithMaxStreamCompletions(budget))
	defer func() { l.Drain() }()
	routes := make(map[takt.RouteID]bool) // false=active, true=cancel requested
	handles := []takt.Subscription[int]{{}}
	var fatal error
	drain := func() {
		t.Helper()
		calls := b.cancelCalls
		for _, canceling := range routes {
			if !canceling {
				calls++
			}
		}
		if n := l.Drain(); n != len(routes) || b.cancelCalls != calls {
			t.Fatalf("drain count=%d/%d cancel calls=%d/%d", n, len(routes), b.cancelCalls, calls)
		}
		clear(routes)
		if fatal == nil {
			fatal = takt.ErrDisposed
		}
	}
	for turn := range max(64, len(data)*2) {
		switch in.next() % 5 {
		case 0, 1:
			// Choose generations that put every reused token in bucket zero.
			token := takt.Token(turn % 4)
			hash := collisionToken(uint64(turn + 1))
			generation := bits.RotateLeft64(uint64(hash^token), -32)
			id := takt.NewRouteID(token, generation)
			b.ids = []takt.RouteID{id}
			b.subscribeErr = nil
			switch in.next() % 13 {
			case 0:
				b.subscribeErr = iox.ErrWouldBlock
			case 1:
				b.subscribeErr = historyFailure
			}
			want := fatal
			if want == nil {
				if len(routes) == capacity {
					want = iox.ErrWouldBlock
				} else {
					want = b.subscribeErr
				}
			}
			sub, err := l.Subscribe(echoOp{})
			if err != want || (err != nil && !sub.IsZero()) {
				t.Fatalf("subscribe=%v, want %v", err, want)
			}
			if err == nil {
				if sub.ID() != id {
					t.Fatal("lost originating identity")
				}
				routes[id] = false
				handles = append(handles, sub)
			} else if len(b.ids) != 1 {
				t.Fatal("refused admission consumed the provider route")
			}
		case 2:
			sub := handles[int(in.next())%len(handles)]
			b.cancelErr = nil
			if in.next()%3 == 0 {
				b.cancelErr = iox.ErrWouldBlock
			}
			want, calls := fatal, b.cancelCalls
			canceling, exists := routes[sub.ID()]
			if want == nil {
				switch {
				case !exists:
					want = takt.ErrUnknownSubscription
				case !canceling:
					calls++
					want = b.cancelErr
				}
			}
			if err := l.Cancel(sub); err != want || b.cancelCalls != calls {
				t.Fatalf("cancel=%v/%v calls=%d/%d", err, want, b.cancelCalls, calls)
			}
			if want == nil {
				routes[sub.ID()] = true
			}
		default:
			b.ready = nil
			b.pollErr = nil
			switch in.next() % 61 {
			case 0:
				b.pollErr = historyFailure
			case 1, 2:
				b.pollErr = iox.ErrWouldBlock
			}
			var want []takt.StreamEvent[int]
			if b.pollErr == nil {
				for range int(in.next()) % (budget + 1) {
					sub := handles[int(in.next())%len(handles)]
					flags := in.next()
					c := takt.StreamCompletion[int]{ID: sub.ID(), Value: turn, More: flags&1 != 0, HasValue: flags&2 != 0}
					if flags&4 != 0 {
						c.EventErr = historyFailure
					}
					b.ready = append(b.ready, c)
					if _, exists := routes[c.ID]; exists && fatal == nil {
						want = append(want, takt.StreamEvent[int]{Subscription: sub, Value: c.Value, HasValue: c.HasValue, Final: !c.More, EventErr: c.EventErr})
						if !c.More {
							delete(routes, c.ID)
						}
					}
				}
			}
			if fatal == nil && b.pollErr == historyFailure {
				fatal = b.pollErr
				clear(routes)
			}
			events, err := l.Poll()
			if err != fatal || !slices.Equal(events, want) {
				t.Fatalf("poll=%v/%v events=%v/%v", err, fatal, events, want)
			}
		}
		if l.Pending() != len(routes) || l.Failed() != fatal {
			t.Fatalf("pending=%d/%d fatal=%v/%v", l.Pending(), len(routes), l.Failed(), fatal)
		}
		if turn%31 == 30 {
			drain()
			b = &historyStream{}
			l = takt.NewSubscriptionLoop[*historyStream, int](b, capacity, takt.WithMaxStreamCompletions(budget))
			handles = handles[:1]
			fatal = nil
		}
	}
	drain()
	drain()
}

func TestStreamHistories(t *testing.T) {
	for seed := range uint64(32) {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		data := make([]byte, 1024)
		for i := range data {
			data[i] = byte(rng.Uint32())
		}
		checkStreamHistory(t, data)
	}
}

func FuzzStreamHistory(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{15, 1, 5, 6, 7, 8, 9})
	f.Add([]byte{7, 2, 0, 5, 17, 18, 255})
	f.Fuzz(checkStreamHistory)
}
