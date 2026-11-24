// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"fmt"
	"testing"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

type performanceBackend struct {
	ready    []takt.Token
	n        int
	sequence takt.Token
	stride   takt.Token
	refuse   bool
}

func (p *performanceBackend) Submit(kont.Operation) (takt.Token, error) {
	if p.refuse {
		return 0, iox.ErrWouldBlock
	}
	p.sequence += p.stride
	p.ready[p.n] = p.sequence
	p.n++
	return p.sequence, nil
}

func (p *performanceBackend) Poll(out []takt.Completion) (int, error) {
	n := min(p.n, len(out))
	for i := range n {
		p.n--
		out[i] = takt.Completion{Token: p.ready[p.n], Value: byte(7)}
	}
	return n, nil
}

func BenchmarkLoopSubmit(b *testing.B) {
	backend := &performanceBackend{ready: make([]takt.Token, 1), stride: 1}
	loop := takt.NewLoop[*performanceBackend, byte](backend, 1, takt.WithMaxCompletions(1))
	defer loop.Drain()

	b.ReportAllocs()
	for b.Loop() {
		if _, done, err := loop.Submit(kont.Perform(performanceOp(0))); err != nil || done {
			b.Fatalf("Submit returned done=%v, err=%v", done, err)
		}
		results, err := loop.Poll()
		if err != nil || len(results) != 1 || results[0] != 7 {
			b.Fatalf("Poll returned %v, %v", results, err)
		}
	}
}

// One owner per instance. Refused work stays live; Poll measures bounded retry
// scheduling, not sleeping or provider acceptance latency.
func BenchmarkLoopRefusal(b *testing.B) {
	for _, capacity := range []int{64, 1024} {
		for _, budget := range []int{1, 64} {
			b.Run(fmt.Sprintf("capacity=%d/budget=%d", capacity, budget), func(b *testing.B) {
				p := &performanceBackend{refuse: true}
				l := takt.NewLoop[*performanceBackend, byte](p, capacity, takt.WithMaxCompletions(budget))
				defer l.Drain()
				m := kont.ExprPerform(performanceOp(0))
				for range capacity {
					if _, _, err := l.SubmitExpr(m); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				for b.Loop() {
					if _, err := l.Poll(); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*budget), "ns/offer")
			})
		}
	}
}

func BenchmarkLoopBatch(b *testing.B) {
	for _, count := range []int{1, 64, 1024, 2048, 8192, 32768} {
		for _, stride := range []takt.Token{1, 64} {
			b.Run(fmt.Sprintf("count=%d/stride=%d", count, stride), func(b *testing.B) {
				p := &performanceBackend{ready: make([]takt.Token, count), stride: stride}
				l := takt.NewLoop[*performanceBackend, byte](p, count, takt.WithMaxCompletions(count))
				defer l.Drain()
				m := kont.ExprPerform(performanceOp(0))
				b.ReportAllocs()
				for b.Loop() {
					for range count {
						if _, done, err := l.SubmitExpr(m); err != nil || done {
							b.Fatal("admission failed", err)
						}
					}
					results, err := l.Poll()
					if err != nil || len(results) != count {
						b.Fatal("lost batch", err)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*count), "ns/result")
			})
		}
	}
}
