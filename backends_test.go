// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

// Shared Backend / CompletionMemory test doubles for Loop tests. Dispatcher/Operation
// fixtures live in fixtures_test.go.

import (
	"errors"

	"code.hybscloud.com/iox"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

// immediateBackend dispatches operations synchronously via a dispatch function:
// Submit queues the completion; Poll returns queued completions.
type immediateBackend struct {
	dispatch func(op kont.Operation) (kont.Resumed, error)
	nextTok  takt.Token
	ready    []takt.Completion
}

func (b *immediateBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	v, err := b.dispatch(op)
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: v,
		Err:   err,
	})
	return tok, nil
}

func (b *immediateBackend) Poll(completions []takt.Completion) (int, error) {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

// failSubmitBackend fails on the failAt-th Submit call.
type failSubmitBackend struct {
	dispatch func(op kont.Operation) (kont.Resumed, error)
	nextTok  takt.Token
	ready    []takt.Completion
	failAt   int
	submits  int
}

func (b *failSubmitBackend) Submit(op kont.Operation) (takt.Token, error) {
	b.submits++
	if b.submits == b.failAt {
		return 0, errors.New("takt_test: submit failed")
	}
	tok := b.nextTok
	b.nextTok++
	v, err := b.dispatch(op)
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: v,
		Err:   err,
	})
	return tok, nil
}

func (b *failSubmitBackend) Poll(completions []takt.Completion) (int, error) {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

// errMoreBackend marks each completion with iox.ErrMore.
type errMoreBackend struct {
	nextTok takt.Token
	ready   []takt.Completion
}

func (b *errMoreBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	if _, ok := op.(echoOp); ok {
		b.ready = append(b.ready, takt.Completion{
			Token: tok,
			Value: 10,
			Err:   iox.ErrMore,
		})
	}
	return tok, nil
}

func (b *errMoreBackend) Poll(completions []takt.Completion) (int, error) {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

// failureBackend reports a non-iox infrastructure failure in completions.
type failureBackend struct {
	nextTok takt.Token
	ready   []takt.Completion
}

func (b *failureBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: -1,
		Err:   errors.New("takt_test: device error"),
	})
	return tok, nil
}

func (b *failureBackend) Poll(completions []takt.Completion) (int, error) {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

// wouldBlockCompletionBackend reports iox.ErrWouldBlock on the first
// completion of each token, then completes successfully on resubmission.
type wouldBlockCompletionBackend struct {
	nextTok  takt.Token
	attempts int
	ready    []takt.Completion
}

func (b *wouldBlockCompletionBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	b.attempts++
	if b.attempts == 1 {
		b.ready = append(b.ready, takt.Completion{
			Token: tok,
			Value: 0,
			Err:   iox.ErrWouldBlock,
		})
		return tok, nil
	}
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: 10,
		Err:   nil,
	})
	return tok, nil
}

func (b *wouldBlockCompletionBackend) Poll(completions []takt.Completion) (int, error) {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

// pollErrorBackend always reports a fixed non-iox error from Poll.
type pollErrorBackend struct {
	nextTok takt.Token
	pollErr error
}

func (b *pollErrorBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	return tok, nil
}

func (b *pollErrorBackend) Poll(completions []takt.Completion) (int, error) {
	return 0, b.pollErr
}

// idleThenReadyBackend exercises Loop's iox.ErrWouldBlock-as-idle handling on
// the first poll, then delivers ready completions on subsequent polls.
type idleThenReadyBackend struct {
	nextTok takt.Token
	ready   []takt.Completion
	polls   int
}

func (b *idleThenReadyBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: 21,
		Err:   nil,
	})
	return tok, nil
}

func (b *idleThenReadyBackend) Poll(completions []takt.Completion) (int, error) {
	b.polls++
	if b.polls == 1 {
		return 0, iox.ErrWouldBlock
	}
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

// observingBackend records the buffer length passed to Poll (used by CompletionMemory
// provider tests to assert wiring).
type observingBackend struct {
	seenBufLen int
}

func (b *observingBackend) Submit(op kont.Operation) (takt.Token, error) {
	return 0, nil
}

func (b *observingBackend) Poll(completions []takt.Completion) (int, error) {
	b.seenBufLen = len(completions)
	return 0, nil
}

// multishotBackend reports two completions on the same token to simulate a
// multishot source: an iox.ErrMore intermediate completion followed by a
// terminal nil. With maxCompletions=1, only the first completion surfaces per
// poll, exercising the same-token continuation path.
type multishotBackend struct {
	nextTok takt.Token
	ready   []takt.Completion
}

func (b *multishotBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.nextTok
	b.nextTok++
	b.ready = append(b.ready,
		takt.Completion{Token: tok, Value: 10, Err: iox.ErrMore},
		takt.Completion{Token: tok, Value: 20, Err: nil},
	)
	return tok, nil
}

func (b *multishotBackend) Poll(completions []takt.Completion) (int, error) {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

// duplicateTokenBackend returns a caller-provided token schedule so tests can
// verify that Loop rejects reuse of a still-live token.
type duplicateTokenBackend struct {
	tokens []takt.Token
	next   int
	value  kont.Resumed
	ready  []takt.Completion
}

func (b *duplicateTokenBackend) Submit(op kont.Operation) (takt.Token, error) {
	tok := b.tokens[b.next]
	b.next++
	b.ready = append(b.ready, takt.Completion{
		Token: tok,
		Value: b.value,
		Err:   nil,
	})
	return tok, nil
}

func (b *duplicateTokenBackend) Poll(completions []takt.Completion) (int, error) {
	n := copy(completions, b.ready)
	b.ready = b.ready[n:]
	return n, nil
}

// recordingMemory records each CompletionBuf and Release call to assert that
// CompletionMemory is used through NewLoop with WithMemory. Slab allocation is
// delegated to a private HeapMemory so the option surface is honored without
// test-only introspection.
type recordingMemory struct {
	inner        takt.HeapMemory
	calls        int
	lastN        int
	releaseCalls int
	released     bool
}

func (m *recordingMemory) CompletionBuf(opts ...takt.CompletionBufOption) []takt.Completion {
	m.calls++
	buf := m.inner.CompletionBuf(opts...)
	m.lastN = len(buf)
	return buf
}

func (m *recordingMemory) Release(buf []takt.Completion) {
	m.releaseCalls++
	m.released = true
	m.inner.Release(buf)
}
