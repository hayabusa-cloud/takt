// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"testing"

	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

func TestCompletionResumeValueDirect(t *testing.T) {
	c := takt.Completion{Token: 7, Value: "ready"}
	if got := c.ResumeValue(); got != "ready" {
		t.Fatalf("ResumeValue() = %#v, want ready", got)
	}
}

func TestCompletionResumeValueInline(t *testing.T) {
	c := takt.NewInlineCompletion(9, 17, 23, inlineWordsDecoder{}, nil)

	got, ok := c.ResumeValue().([2]uint64)
	if !ok || got != [2]uint64{17, 23} {
		t.Fatalf("ResumeValue() = (%T,%v), want [2]uint64{17,23}", c.ResumeValue(), c.ResumeValue())
	}
}

var completionSink takt.Completion

type inlineWordsDecoder struct{}

func (inlineWordsDecoder) DecodeCompletion(word0, word1 uint64) kont.Resumed {
	return [2]uint64{word0, word1}
}

func TestNewInlineCompletionDoesNotAllocate(t *testing.T) {
	allocs := testing.AllocsPerRun(1000, func() {
		completionSink = takt.NewInlineCompletion(1, 4096, 8192, inlineWordsDecoder{}, nil)
	})
	if allocs != 0 {
		t.Fatalf("NewInlineCompletion allocations = %v, want 0", allocs)
	}
}
