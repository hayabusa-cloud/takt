// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"errors"
	"fmt"
	"testing"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

type performanceOp byte

func (performanceOp) OpResult() byte { return 0 }

type performanceDispatcher struct{ value byte }

func (p *performanceDispatcher) Dispatch(kont.Operation) (kont.Resumed, error) {
	p.value++
	return p.value, nil
}

type performanceCursor uint64

func (performanceCursor) Op() kont.Operation { return performanceOp(0) }
func (c performanceCursor) Resume(v kont.Resumed) (byte, performanceCursor) {
	return v.(byte), c + 1
}

type performanceErased struct {
	dispatcher interface {
		Dispatch(kont.Operation) (kont.Resumed, error)
	}
}

func (p performanceErased) Dispatch(op kont.Operation) (kont.Resumed, error) {
	return p.dispatcher.Dispatch(op)
}

var performanceResult byte

type performanceControlDispatcher struct {
	control error
	value   byte
}

func (p *performanceControlDispatcher) Dispatch(kont.Operation) (kont.Resumed, error) {
	p.value++
	return p.value, p.control
}

// Wrappers are constructed before timing; the measured path classifies the
// original error and either advances or retains the structural carrier.
func BenchmarkDispatchControl(b *testing.B) {
	for _, tc := range []struct {
		name    string
		control error
	}{
		{"nil", nil},
		{"would-block", iox.ErrWouldBlock},
		{"more", iox.ErrMore},
		{"failure", errors.New("provider failed")},
		{"wrapped-block", fmt.Errorf("pending: %w", iox.ErrWouldBlock)},
		{"wrapped-more", fmt.Errorf("live: %w", iox.ErrMore)},
		{"joined", errors.Join(iox.ErrMore, iox.ErrWouldBlock)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			d := &performanceControlDispatcher{control: tc.control}
			var cursor performanceCursor
			var value byte
			b.ReportAllocs()
			for b.Loop() {
				var err error
				value, cursor, err = takt.AdvanceSuspension(d, cursor)
				if err != tc.control {
					b.Fatal("changed dispatch error", err)
				}
			}
			performanceResult = value
		})
	}
}

// The error-aware wrapper retries one existing capture; construction and
// disposal are outside timing, and no continuation body runs on refusal.
func BenchmarkAdvanceErrorRefusal(b *testing.B) {
	for _, tc := range []struct {
		name    string
		control error
	}{
		{"would-block", iox.ErrWouldBlock},
		{"failure", errors.New("provider failed")},
	} {
		b.Run(tc.name, func(b *testing.B) {
			d := &performanceControlDispatcher{control: tc.control}
			_, susp := takt.StepError[string](kont.ExprPerform(performanceOp(0)))
			defer susp.Discard()
			b.ReportAllocs()
			for b.Loop() {
				_, next, err := takt.AdvanceError(d, susp)
				if next != susp || err != tc.control {
					b.Fatal("changed refused frontier")
				}
			}
		})
	}
}

func BenchmarkDispatchFamily(b *testing.B) {
	b.Run("direct", func(b *testing.B) {
		d := &performanceDispatcher{}
		var cursor performanceCursor
		var value byte
		b.ReportAllocs()
		for b.Loop() {
			v, err := d.Dispatch(cursor.Op())
			if err != nil {
				b.Fatal(err)
			}
			value, cursor = cursor.Resume(v)
		}
		performanceResult = value
	})
	b.Run("typed", func(b *testing.B) {
		d := &performanceDispatcher{}
		var cursor performanceCursor
		var value byte
		b.ReportAllocs()
		for b.Loop() {
			var err error
			value, cursor, err = takt.AdvanceSuspension(d, cursor)
			if err != nil {
				b.Fatal(err)
			}
		}
		performanceResult = value
	})
	b.Run("erased-adapter", func(b *testing.B) {
		d := performanceErased{dispatcher: &performanceDispatcher{}}
		var cursor performanceCursor
		var value byte
		b.ReportAllocs()
		for b.Loop() {
			var err error
			value, cursor, err = takt.AdvanceSuspension(d, cursor)
			if err != nil {
				b.Fatal(err)
			}
		}
		performanceResult = value
	})
}
