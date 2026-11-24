// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"slices"
	"testing"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

func TestPositiveSubscriptionCapacity(t *testing.T) {
	for _, n := range []int{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("accepted nonpositive route capacity")
				}
			}()
			takt.NewSubscriptionLoop[*streamBackend, int](&streamBackend{}, n)
		}()
	}
}

func TestSubscriptionCapacityBeforeOffer(t *testing.T) {
	b := &streamBackend{}
	l := takt.NewSubscriptionLoop[*streamBackend, int](b, 1, takt.WithMaxStreamCompletions(1))
	defer l.Drain()
	s, err := l.Subscribe(echoOp{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Subscribe(echoOp{}); err != iox.ErrWouldBlock || b.nextToken != 1 {
		t.Fatal("route capacity checked after external offer", err)
	}
	b.ready = append(b.ready, takt.StreamCompletion[int]{ID: s.ID()})
	if _, err = l.Poll(); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Subscribe(echoOp{}); err != nil || b.nextToken != 2 {
		t.Fatal("retired route did not restore capacity", err)
	}
}

type observingStreamBackend struct {
	streamBackend
	observe func()
}

func (b *observingStreamBackend) Subscribe(op kont.Operation) (takt.RouteID, error) {
	b.observe()
	return b.streamBackend.Subscribe(op)
}

func (b *observingStreamBackend) Cancel(id takt.RouteID) error {
	b.observe()
	return b.streamBackend.Cancel(id)
}

func TestSubscriptionPendingDuringBackendCalls(t *testing.T) {
	b := &observingStreamBackend{}
	l := takt.NewSubscriptionLoop[*observingStreamBackend, int](b, 2)
	var counts []int
	b.observe = func() { counts = append(counts, l.Pending()) }
	defer l.Drain()

	if _, err := l.Subscribe(echoOp{}); err != nil {
		t.Fatal(err)
	}
	b.subscribeErr = iox.ErrWouldBlock
	if _, err := l.Subscribe(echoOp{}); err != iox.ErrWouldBlock {
		t.Fatal(err)
	}
	if n := l.Drain(); n != 1 {
		t.Fatalf("drained %d routes, want 1", n)
	}
	if want := []int{0, 1, 0}; !slices.Equal(counts, want) {
		t.Fatalf("Pending during accepted offer, refused offer, and disposal = %v, want %v", counts, want)
	}
}
