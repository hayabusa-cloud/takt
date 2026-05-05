// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/kont"
)

// Token correlates a submitted operation with its completion.
// A backend may reuse a token only after the older submission carrying it has
// retired from the [Loop].
type Token uint64

// Completion carries token-correlated backend evidence together with an `iox`
// outcome. Value is valid resumption input for the correlated suspension even
// when Err reports an infrastructure failure.
type Completion struct {
	Token Token
	Value kont.Resumed
	Err   error
}

// Backend is the interface for asynchronous submit/poll execution.
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
	// polling operation itself.
	Poll(completions []Completion) (int, error)
}
