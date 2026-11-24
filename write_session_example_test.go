// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"fmt"

	"code.hybscloud.com/cove"
	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
	"code.hybscloud.com/tempo"
)

type writeEvent uint8

const (
	socketWritable writeEvent = iota
	socketBlocked
	peerGranted
	peerRevoked
)

type writability bool

func (w writability) Observe() bool { return bool(w) }
func (w writability) Step(e writeEvent) writability {
	switch e {
	case socketWritable:
		return true
	case socketBlocked:
		return false
	}
	return w
}

type writePermission bool

func (p writePermission) Observe() bool { return bool(p) }
func (p writePermission) Step(e writeEvent) writePermission {
	switch e {
	case peerGranted:
		return true
	case peerRevoked:
		return false
	}
	return p
}

type writeMessage struct {
	kont.Phantom[int]
	text string
}

// The in-memory send buffer retains the offset across partial writes.
type sendBuffer struct {
	data   []byte
	offset int
}

func (b *sendBuffer) Dispatch(op kont.Operation) (kont.Resumed, error) {
	text := op.(writeMessage).text
	n := min(len(text)-b.offset, cap(b.data)-len(b.data))
	b.data = append(b.data, text[b.offset:b.offset+n]...)
	b.offset += n
	if b.offset < len(text) {
		return nil, iox.ErrWouldBlock
	}
	b.offset = 0
	return len(text), nil
}

// One owner processes events for this write and performs each dispatch inline.
type writeSession struct {
	route   takt.RouteID
	state   tempo.Product[writability, writePermission, writeEvent, bool, bool]
	pending *kont.Suspension[int]
	buffer  sendBuffer
}

func startWrite(route takt.RouteID, text string, capacity int) writeSession {
	_, pending := takt.Step(kont.ExprPerform(writeMessage{text: text}))
	return writeSession{
		route:   route,
		state:   tempo.Both(writability(false), writePermission(false)),
		pending: pending,
		buffer:  sendBuffer{data: make([]byte, 0, capacity)},
	}
}

// done reports whether this event completed the write.
func (s *writeSession) onEvent(route takt.RouteID, e writeEvent) (n int, done bool, err error) {
	if route != s.route || s.pending == nil {
		return 0, false, nil
	}
	s.state = s.state.Step(e)
	checked, ok := cove.CheckSuspension(s.state.Observe(), s.pending, func(obs tempo.Pair[bool, bool]) bool {
		return obs.Left && obs.Right
	})
	if !ok {
		return 0, false, nil
	}
	n, next, err := takt.AdvanceSuspension(&s.buffer, checked)
	s.pending = next.Suspension
	if iox.IsWouldBlock(err) {
		s.state = s.state.Step(socketBlocked)
	}
	return n, next.Suspension == nil, err
}

// A writable notice permits an attempt. The peer must still permit the write
// when the session dispatches it and resumes the suspension.
func ExampleAdvanceSuspension() {
	route := takt.NewRouteID(3, 7)
	s := startWrite(route, "ping", 2)
	s.onEvent(route, peerGranted)
	_, _, err := s.onEvent(route, socketWritable)
	fmt.Printf("buffered %q, waiting %t\n", s.buffer.data, iox.IsWouldBlock(err))

	s.onEvent(route, peerRevoked)
	s.buffer.data = s.buffer.data[:0] // The transport drains the first two bytes.
	s.onEvent(route, socketWritable)
	fmt.Printf("buffered while permission was revoked %q\n", s.buffer.data)

	n, done, err := s.onEvent(route, peerGranted)
	fmt.Printf("buffered %q, total %d bytes, done %t, error %v\n", s.buffer.data, n, done, err)
	// Output:
	// buffered "pi", waiting true
	// buffered while permission was revoked ""
	// buffered "ng", total 4 bytes, done true, error <nil>
}
