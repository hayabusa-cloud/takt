// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"code.hybscloud.com/kont"
)

// Token correlates a submitted operation with its completion.
type Token uint64

// Completion carries a backend result with iox outcome.
type Completion struct {
	Token Token
	Value kont.Resumed
	Err   error
}

// Backend is the F-bounded interface for async submit/poll.
type Backend[B Backend[B]] interface {
	// Submit sends an operation. Returns a correlation token.
	Submit(op kont.Operation) (Token, error)

	// Poll writes ready completions into completions. Returns count.
	Poll(completions []Completion) int
}
