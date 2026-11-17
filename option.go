// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

// Option configures a [Loop] at construction. Pass options to [NewLoop].
//
// Available options:
//   - [WithMemory] supplies a custom [CompletionMemory] provider (defaults to a fresh
//     [HeapMemory]).
//   - [WithMaxCompletions] caps each Poll phase: offers, observations and resumptions
//     (defaults to the CompletionMemory provider's own choice; see
//     [DefaultCompletionBufSize] for the [HeapMemory] and [BoundedMemory]
//     default size).
type Option interface {
	applyLoop(*loopConfig)
}

type loopConfig struct {
	memory         CompletionMemory
	maxCompletions int // 0 ⇒ use CompletionMemory provider default
}

type optionFunc func(*loopConfig)

func (f optionFunc) applyLoop(c *loopConfig) { f(c) }

// WithMemory installs a custom [CompletionMemory] provider on the [Loop]. The
// provider retains ownership of the slab's lifetime: [Loop.Drain] calls
// [CompletionMemory.Release] exactly once.
func WithMemory(m CompletionMemory) Option {
	if m == nil {
		panic("takt: WithMemory requires a non-nil CompletionMemory")
	}
	return optionFunc(func(c *loopConfig) { c.memory = m })
}

// WithMaxCompletions caps the per-poll completion slab length and the number
// of offers, observations and resumptions in their respective phases. If omitted,
// the CompletionMemory provider chooses this bound (typically
// [DefaultCompletionBufSize]). Panics if n <= 0.
func WithMaxCompletions(n int) Option {
	if n <= 0 {
		panic("takt: WithMaxCompletions requires n > 0")
	}
	return optionFunc(func(c *loopConfig) { c.maxCompletions = n })
}

func resolveLoopConfig(opts []Option) loopConfig {
	cfg := loopConfig{}
	for _, o := range opts {
		o.applyLoop(&cfg)
	}
	if cfg.memory == nil {
		cfg.memory = &HeapMemory{}
	}
	return cfg
}
