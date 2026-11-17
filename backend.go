// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/kont"
)

// Token correlates a submitted operation with its completion.
// A backend may reuse a token only after the older submission carrying it has
// retired from the [Loop]. It must also ensure that delayed observations and
// control references cannot be mistaken for the new submission.
type Token uint64

// CompletionDecoder materializes an inline completion payload at the
// resumption boundary. Implementations should be stateless comparable values
// so constructing a completion remains allocation-free and Completion retains
// its ordinary comparison behavior.
type CompletionDecoder interface {
	DecodeCompletion(word0, word1 uint64) kont.Resumed
}

// Completion carries token-correlated backend evidence together with an `iox`
// outcome. When Err is nil or an ordinary failure, [Completion.ResumeValue]
// supplies the correlated suspension's valid reply, including operation-error
// information. WouldBlock and More do not authorize resumption in [Loop].
// Value is the direct fallback used by ordinary backends. A backend with a
// fixed non-pointer result may use [NewInlineCompletion] to avoid boxing while
// writing the completion slab.
type Completion struct {
	Token Token
	Value kont.Resumed
	Err   error

	inline  [2]uint64
	decoder CompletionDecoder
}

// NewInlineCompletion constructs a completion whose fixed payload is decoded
// only when [Completion.ResumeValue] is called. decoder should be a stateless
// comparable value when allocation-free construction is required.
func NewInlineCompletion(token Token, word0, word1 uint64, decoder CompletionDecoder, err error) Completion {
	return Completion{
		Token:   token,
		Err:     err,
		inline:  [2]uint64{word0, word1},
		decoder: decoder,
	}
}

// ResumeValue returns the payload to pass to the correlated suspension.
// Ordinary completions return Value directly. Inline completions materialize
// their fixed payload at this resumption boundary.
func (c Completion) ResumeValue() kont.Resumed {
	if c.decoder != nil {
		return c.decoder.DecodeCompletion(c.inline[0], c.inline[1])
	}
	return c.Value
}

// Backend is the interface for asynchronous submit/poll execution.
// Submit errors establish that the attempt accepted no work. WouldBlock retains
// the reached suspension for retry; ordinary errors abort the Loop. Operation
// errors instead belong in valid completion replies. Native request and buffer
// retirement remain with the binding after local abandonment. Replies must admit
// delayed delivery and Loop's abort disposition; resource-producing values need
// an independent owner or disposer when undelivered.
// Poll reports whether the poll call itself succeeded separately from which
// token-correlated completions were written into the provided buffer. A backend
// must not return n > 0 and err != nil together; [Loop] handles poll errors
// separately from the per-completion outcome recorded in each [Completion].
type Backend[B Backend[B]] interface {
	// Submit sends an operation and returns a correlation token.
	// Returned tokens must be unique among all submissions that are still live in
	// the [Loop]; once a submission has retired, the backend may reuse its token.
	// Tokens are correlation keys, not sequence numbers, so a concrete backend may
	// use a kernel user_data value directly.
	Submit(op kont.Operation) (Token, error)

	// Poll writes ready completions into completions and reports any
	// infrastructure wait failure. Per-completion outcomes are reported in
	// [Completion.Err]; poll-level errors are reserved for failures of the
	// polling operation itself. It returns 0 <= n <= len(completions) and
	// initializes every entry in completions[:n]. If valid observations precede
	// a service failure, return that batch with nil before reporting the failure.
	Poll(completions []Completion) (int, error)
}
