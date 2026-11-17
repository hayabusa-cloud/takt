// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

var historyFailure = errors.New("history service failed")

type historyInput struct {
	data []byte
	pos  int
}

func (s *historyInput) next() byte {
	if len(s.data) == 0 {
		return 0
	}
	v := s.data[s.pos%len(s.data)]
	s.pos++
	return v
}

type historyOp struct{ job, step, steps int }

func (historyOp) OpResult() int { return 0 }
func (o historyOp) reply() int  { return o.job*8 + o.step }

type historyOffer struct {
	op    historyOp
	token takt.Token
	err   error
}

// This provider implements a finite scalar protocol. Its input controls
// acceptance, response order and service faults; it models no kernel operation.
type historyBackend struct {
	input    historyInput
	sequence int
	live     []historyOffer
	offers   []historyOffer
	batch    []takt.Completion
	pollErr  error
}

func (b *historyBackend) Submit(op kont.Operation) (takt.Token, error) {
	x := historyOffer{op: op.(historyOp)}
	switch b.input.next() % 19 {
	case 0, 1, 2, 3, 4:
		x.err = iox.ErrWouldBlock
	case 18:
		x.err = historyFailure
	default:
		b.sequence++
		x.token = collisionToken(uint64(b.sequence))
		b.live = append(b.live, x)
	}
	b.offers = append(b.offers, x)
	return x.token, x.err
}

func (b *historyBackend) Poll(dst []takt.Completion) (int, error) {
	switch b.input.next() % 31 {
	case 0:
		b.pollErr = historyFailure
		return 0, b.pollErr
	case 1, 2, 3:
		b.pollErr = iox.ErrWouldBlock
		return 0, b.pollErr
	}
	n := min(int(b.input.next())%(len(dst)+1), len(b.live))
	for i := range n {
		j := int(b.input.next()) % len(b.live)
		x := b.live[j]
		b.live = slices.Delete(b.live, j, j+1)
		c := takt.Completion{Token: x.token, Value: x.op.reply()}
		switch b.input.next() % 17 {
		case 0, 1, 2:
			c.Err = iox.ErrWouldBlock
		case 3:
			c.Err = iox.ErrMore
		case 4:
			c.Err = historyFailure // an ordinary error with a valid scalar reply
		}
		dst[i] = c
		b.batch = append(b.batch, c)
	}
	return n, nil
}

type historyQueued struct {
	op       historyOp
	deferred bool
}

// The oracle uses lists and maps, with no arena, bucket chains or ring indices.
// It consumes provider observations and predicts externally visible traces.
type historyModel struct {
	queued  []historyQueued
	active  map[takt.Token]historyOp
	pending int
	fatal   error
}

func (m *historyModel) round(t *testing.T, b *historyBackend, budget int) (resumed []historyOp, results []int) {
	t.Helper()
	if m.fatal != nil {
		if len(b.offers) != 0 || len(b.batch) != 0 {
			t.Fatal("disposed runner touched the provider")
		}
		return
	}
	attempts := min(len(m.queued), budget)
	for i, x := range b.offers {
		if i >= attempts || m.queued[0].op != x.op {
			t.Fatalf("offer %d violated FIFO snapshot: %+v", i, x)
		}
		m.queued = m.queued[1:]
		if x.err == iox.ErrWouldBlock {
			m.queued = append(m.queued, historyQueued{x.op, true})
		} else if x.err != nil {
			m.fatal = x.err
			break
		} else {
			m.active[x.token] = x.op
		}
	}
	if m.fatal == nil && len(b.offers) != attempts {
		t.Fatalf("offers=%d, want %d", len(b.offers), attempts)
	}
	if m.fatal == nil && b.pollErr != nil && b.pollErr != iox.ErrWouldBlock {
		m.fatal = b.pollErr
	}
	for _, c := range b.batch {
		if m.fatal != nil {
			break
		}
		op, exists := m.active[c.Token]
		if !exists {
			t.Fatalf("provider returned an unowned token %d", c.Token)
		}
		delete(m.active, c.Token)
		switch c.Err {
		case iox.ErrWouldBlock:
			m.queued = append(m.queued, historyQueued{op, true})
		case iox.ErrMore:
			m.fatal = takt.ErrUnsupportedMultishot
		default:
			resumed = append(resumed, op)
		}
	}
	for _, op := range resumed {
		if op.step+1 == op.steps {
			m.pending--
			results = append(results, op.reply())
		} else if m.fatal == nil {
			op.step++
			m.queued = append(m.queued, historyQueued{op, false})
		}
	}
	if m.fatal != nil {
		m.pending = 0
		m.queued = nil
		clear(m.active)
	}
	return
}

func checkLoopHistory(t *testing.T, data []byte) {
	t.Helper()
	if len(data) > 1024 {
		data = data[:1024]
	}
	in := historyInput{data: data}
	capacity, budget := int(in.next()%32)+1, int(in.next()%8)+1
	b := &historyBackend{input: historyInput{data: data}}
	l := takt.NewLoop[*historyBackend, int](b, capacity, takt.WithMaxCompletions(budget))
	defer l.Drain()
	m := historyModel{active: make(map[takt.Token]historyOp)}
	var observed []historyOp
	var program func(historyOp) kont.Expr[int]
	program = func(op historyOp) kont.Expr[int] {
		return kont.ExprBind(kont.ExprPerform(op), func(value int) kont.Expr[int] {
			if value != op.reply() {
				t.Fatalf("reply %d reached wrong capture %+v", value, op)
			}
			observed = append(observed, op)
			if op.step+1 == op.steps {
				return kont.ExprReturn(value)
			}
			op.step++
			return program(op)
		})
	}
	for turn := range max(64, len(data)*2) {
		if in.next()%3 != 0 {
			op := historyOp{job: turn + 1, steps: int(in.next()%4) + 1}
			// Rejected captures remain caller-owned and can be disposed immediately.
			_, s := takt.Step(program(op))
			err := l.SubmitSuspension(s)
			want := m.fatal
			if want == nil && m.pending == capacity {
				want = iox.ErrWouldBlock
			}
			if err != want {
				t.Fatalf("admission=%v, want %v", err, want)
			}
			if err != nil {
				s.Discard()
			} else {
				m.pending++
				m.queued = append(m.queued, historyQueued{op, false})
			}
		}
		b.offers, b.batch, b.pollErr, observed = b.offers[:0], b.batch[:0], nil, observed[:0]
		results, err := l.Poll()
		resumed, want := m.round(t, b, budget)
		wantErr := m.fatal
		if len(want) != 0 {
			wantErr = nil // a completed prefix is observable before the fatal error
		}
		if !slices.Equal(results, want) || !slices.Equal(observed, resumed) || err != wantErr {
			t.Fatalf("turn=%d result=%v/%v resume=%v/%v err=%v/%v", turn, results, want, observed, resumed, err, wantErr)
		}
		ready := slices.ContainsFunc(m.queued, func(q historyQueued) bool { return !q.deferred })
		if l.Pending() != m.pending || l.Ready() != ready || l.Failed() != m.fatal {
			t.Fatalf("turn=%d pending=%d/%d ready=%v/%v fatal=%v/%v", turn, l.Pending(), m.pending, l.Ready(), ready, l.Failed(), m.fatal)
		}
		if m.fatal != nil && turn%7 == 0 {
			if l.Drain() != 0 {
				t.Fatal("fatal runner retained captures")
			}
			b = &historyBackend{input: in}
			l = takt.NewLoop[*historyBackend, int](b, capacity, takt.WithMaxCompletions(budget))
			defer l.Drain()
			m = historyModel{active: make(map[takt.Token]historyOp)}
		}
	}
	n := l.Pending()
	if l.Drain() != n || l.Drain() != 0 || l.Ready() || l.Pending() != 0 {
		t.Fatal("drain did not dispose each remaining lineage once")
	}
}

func TestLoopHistories(t *testing.T) {
	for seed := range uint64(32) {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		data := make([]byte, 1024)
		for i := range data {
			data[i] = byte(rng.Uint32())
		}
		checkLoopHistory(t, data)
	}
}

func FuzzLoopHistory(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{31, 1, 5, 6, 7, 8, 9})
	f.Add([]byte{7, 2, 0, 5, 17, 18, 255})
	f.Fuzz(checkLoopHistory)
}
