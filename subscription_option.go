// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

// SubscriptionOption configures a [SubscriptionLoop] at construction.
type SubscriptionOption interface {
	applySubscription(*subscriptionConfig)
}

type subscriptionConfig struct {
	maxCompletions int
}

type subscriptionOptionFunc func(*subscriptionConfig)

func (f subscriptionOptionFunc) applySubscription(c *subscriptionConfig) { f(c) }

// WithMaxStreamCompletions caps the per-poll stream completion buffer length.
// If omitted, [DefaultCompletionBufSize] is used. Panics if n <= 0.
func WithMaxStreamCompletions(n int) SubscriptionOption {
	if n <= 0 {
		panic("takt: WithMaxStreamCompletions requires n > 0")
	}
	return subscriptionOptionFunc(func(c *subscriptionConfig) { c.maxCompletions = n })
}

func resolveSubscriptionConfig(opts []SubscriptionOption) subscriptionConfig {
	cfg := subscriptionConfig{maxCompletions: DefaultCompletionBufSize}
	for _, o := range opts {
		o.applySubscription(&cfg)
	}
	return cfg
}
