// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"fmt"
	"testing"

	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

type performanceStream struct {
	ids    []takt.RouteID
	next   int
	cursor int
}

func (p *performanceStream) Subscribe(kont.Operation) (takt.RouteID, error) {
	id := p.ids[p.next]
	p.next++
	return id, nil
}

func (p *performanceStream) Cancel(takt.RouteID) error { return nil }

func (p *performanceStream) Poll(out []takt.StreamCompletion[byte]) (int, error) {
	for i := range out {
		out[i] = takt.StreamCompletion[byte]{ID: p.ids[p.cursor], Value: 7, HasValue: true, More: true}
		p.cursor++
		if p.cursor == len(p.ids) {
			p.cursor = 0
		}
	}
	return len(out), nil
}

func benchmarkSubscriptionRoutes(b *testing.B, run func(*testing.B, []takt.RouteID)) {
	for _, count := range []int{1, 64, 1024, 2048, 8192, 32768} {
		for _, stride := range []takt.Token{1, 64} {
			b.Run(fmt.Sprintf("routes=%d/stride=%d", count, stride), func(b *testing.B) {
				ids := make([]takt.RouteID, count)
				for i := range ids {
					ids[i] = takt.NewRouteID(takt.Token(i+1)*stride, 1)
				}
				run(b, ids)
			})
		}
	}
	b.Run("routes=1024/token=generation", func(b *testing.B) {
		ids := make([]takt.RouteID, 1024)
		for i := range ids {
			ids[i] = takt.NewRouteID(takt.Token(i+1), uint64(i+1))
		}
		run(b, ids)
	})
}

func BenchmarkSubscriptionLocality(b *testing.B) {
	benchmarkSubscriptionRoutes(b, func(b *testing.B, ids []takt.RouteID) {
		const batch = 64
		p := &performanceStream{ids: ids}
		l := takt.NewSubscriptionLoop[*performanceStream, byte](p, len(ids), takt.WithMaxStreamCompletions(batch))
		defer l.Drain()
		for range ids {
			if _, err := l.Subscribe(performanceOp(0)); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportAllocs()
		for b.Loop() {
			events, err := l.Poll()
			if err != nil || len(events) != batch {
				b.Fatal("lost events", err)
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batch), "ns/event")
	})
}

type performanceFinalStream struct{ performanceStream }

func (p *performanceFinalStream) Poll(out []takt.StreamCompletion[byte]) (int, error) {
	n := min(len(out), p.next-p.cursor)
	for i := range n {
		out[i] = takt.StreamCompletion[byte]{ID: p.ids[p.cursor], Value: 7, HasValue: true}
		p.cursor++
	}
	return n, nil
}

// Each cycle admits and retires every route. This closed owner has no late
// observations; identities are reused only after the whole cycle has settled.
func BenchmarkSubscriptionRetirement(b *testing.B) {
	benchmarkSubscriptionRoutes(b, func(b *testing.B, ids []takt.RouteID) {
		const batch = 64
		p := &performanceFinalStream{performanceStream{ids: ids}}
		l := takt.NewSubscriptionLoop[*performanceFinalStream, byte](p, len(ids), takt.WithMaxStreamCompletions(batch))
		defer l.Drain()
		b.ReportAllocs()
		for b.Loop() {
			p.next, p.cursor = 0, 0
			for range ids {
				if _, err := l.Subscribe(performanceOp(0)); err != nil {
					b.Fatal(err)
				}
			}
			for remaining := len(ids); remaining > 0; remaining -= batch {
				events, err := l.Poll()
				if err != nil || len(events) != min(batch, remaining) {
					b.Fatal("lost terminal events", err)
				}
			}
			if l.Pending() != 0 {
				b.Fatal("retained terminal route")
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(ids)), "ns/route")
	})
}
