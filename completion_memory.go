// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

import (
	"sync"
	"unsafe"

	"code.hybscloud.com/iobuf"
)

// CompletionMemory provides the [Completion] buffer used by a [Loop].
//
// It is consulted twice per [Loop] lifetime: once at construction
// ([CompletionMemory.CompletionBuf]) and once at termination
// ([CompletionMemory.Release]). It is a loop-level resource rather than a
// per-dispatch callback, so implementations can choose any allocation strategy
// that fits their workload.
//
// CompletionMemory only manages storage for observed completions. It does not
// change [Backend], [Token], [Completion], or the outcome semantics reported
// by the backend.
//
// takt ships with two built-ins: [HeapMemory] (the implicit default of
// [NewLoop]) and [BoundedMemory] (a single bounded pool of default-sized 128
// KiB slabs). Custom providers may implement CompletionMemory directly.
//
// Requirements:
//   - when [WithSize] is supplied, CompletionBuf must return at least that
//     many slots; [Loop] trims the visible length back to the requested size
//     so [WithMaxCompletions] remains a true cap.
//   - when no size hint is supplied, CompletionBuf must return a non-empty
//     default slab.
//
// The slice passed to Release must have been obtained from CompletionBuf on the
// same CompletionMemory and must no longer be aliased by the caller.
type CompletionMemory interface {
	CompletionBuf(opts ...CompletionBufOption) []Completion
	Release(buf []Completion)
}

// CompletionBufOption tunes a single [CompletionMemory.CompletionBuf] call.
type CompletionBufOption interface {
	applyCompletionBuf(*completionBufConfig)
}

type completionBufConfig struct {
	size int // 0 ⇒ provider default
}

type completionBufOptionFunc func(*completionBufConfig)

func (f completionBufOptionFunc) applyCompletionBuf(c *completionBufConfig) { f(c) }

// WithSize requests an explicit completion-slab length. Providers may round
// up to an internal storage boundary; the returned slice still satisfies
// `len >= n`. Panics if n <= 0; omit [WithSize] to request the provider's
// default slab length.
func WithSize(n int) CompletionBufOption {
	if n <= 0 {
		panic("takt: WithSize requires n > 0")
	}
	return completionBufOptionFunc(func(c *completionBufConfig) { c.size = n })
}

func resolveCompletionBufConfig(opts []CompletionBufOption, fallback int) completionBufConfig {
	cfg := completionBufConfig{size: 0}
	for _, o := range opts {
		o.applyCompletionBuf(&cfg)
	}
	if cfg.size == 0 {
		cfg.size = fallback
	}
	return cfg
}

// completionSize is the byte size of one Completion entry, fixed at
// compile time on the target architecture.
const completionSize = unsafe.Sizeof(Completion{})

// DefaultCompletionBufBytes anchors the default completion slab byte budget
// to iobuf's canonical "register buffer" size class
// ([iobuf.BufferSizeLarge], 128 KiB).
const DefaultCompletionBufBytes = iobuf.BufferSizeLarge

const defaultCompletionBufSize = DefaultCompletionBufBytes / int(completionSize)

type defaultCompletionSlab [defaultCompletionBufSize]Completion

// DefaultCompletionBufSize is the number of [Completion] entries that fit in
// one [DefaultCompletionBufBytes] block. This is the slab length [HeapMemory]
// returns from [HeapMemory.CompletionBuf] when no [WithSize] option is
// supplied, and the implicit [NewLoop] choice when [WithMaxCompletions] is
// not given.
const DefaultCompletionBufSize = defaultCompletionBufSize

// HeapMemory is the default [CompletionMemory]: a [sync.Pool]-backed cache of
// default-sized typed [Completion] slabs. Slabs of non-default length bypass
// the pool and are freshly allocated each call.
//
// Pass `&takt.HeapMemory{}` (or the address of an existing HeapMemory
// variable) to [WithMemory] so the underlying pool is shared across Loop
// instances. Copying a HeapMemory value copies the embedded [sync.Pool] state
// and therefore does not share recycled slabs.
type HeapMemory struct {
	pool sync.Pool
}

var _ CompletionMemory = (*HeapMemory)(nil)

// CompletionBuf returns a [Completion] slab of the requested length. The
// default length is [DefaultCompletionBufSize]; only default-sized slabs
// participate in the internal [sync.Pool].
func (h *HeapMemory) CompletionBuf(opts ...CompletionBufOption) []Completion {
	cfg := resolveCompletionBufConfig(opts, DefaultCompletionBufSize)
	if cfg.size == DefaultCompletionBufSize {
		if v := h.pool.Get(); v != nil {
			slab := v.(*defaultCompletionSlab)
			s := slab[:cfg.size]
			clearCompletions(s)
			return s
		}
		return new(defaultCompletionSlab)[:cfg.size]
	}
	return make([]Completion, cfg.size)
}

// Release returns the slab to the internal pool when its capacity matches
// [DefaultCompletionBufSize]; non-default slabs are left for the GC to reclaim.
func (h *HeapMemory) Release(buf []Completion) {
	if cap(buf) != DefaultCompletionBufSize {
		return
	}
	full := buf[:cap(buf)]
	clearCompletions(full)
	h.pool.Put((*defaultCompletionSlab)(unsafe.Pointer(unsafe.SliceData(full))))
}

// BoundedMemory is an `iobuf`-backed [CompletionMemory] provider for steady-state
// workloads.
//
// It keeps a single bounded pool of default-sized 128 KiB slabs backed by
// iobuf's lock-free MPMC pool ([iobuf.BoundedPool]). Requests up to
// [DefaultCompletionBufSize] reuse the default slab shape; larger requests
// fall back to fresh exact-size allocations. Overflow slabs are discarded in
// [Release] so the bounded memory budget stays anchored to the
// steady-state default slab.
type BoundedMemory struct {
	poolCapacity int
	initOnce     sync.Once
	pool         boundedPoolState
}

var _ CompletionMemory = (*BoundedMemory)(nil)

type boundedPoolState struct {
	pool    *iobuf.BoundedPool[*poolSlab]
	bySlab  map[unsafe.Pointer]*poolSlab // read-only after Fill: data ptr -> slot
	backing []Completion
}

type poolSlab struct {
	idx  int
	data []Completion
}

// DefaultPoolCapacity is the default bounded-pool capacity used by
// [BoundedMemory] when [WithPoolCapacity] is not supplied. `iobuf` rounds the
// configured capacity up to the next power of two. With
// [DefaultCompletionBufBytes] set to [iobuf.BufferSizeLarge], the default
// bounded pool keeps a contiguous 1 MiB backing block of 8 slabs.
const DefaultPoolCapacity = 8

// BoundedMemoryOption configures a [BoundedMemory] at construction.
type BoundedMemoryOption interface {
	applyBoundedMemory(*BoundedMemory)
}

type boundedMemoryOptionFunc func(*BoundedMemory)

func (f boundedMemoryOptionFunc) applyBoundedMemory(b *BoundedMemory) { f(b) }

// WithPoolCapacity sets the bounded-pool capacity for [BoundedMemory]'s
// single default-sized slab pool. The capacity is rounded up to the next
// power of two by iobuf. Panics if n < 1.
func WithPoolCapacity(n int) BoundedMemoryOption {
	if n < 1 {
		panic("takt: WithPoolCapacity requires n >= 1")
	}
	return boundedMemoryOptionFunc(func(b *BoundedMemory) { b.poolCapacity = n })
}

// NewBoundedMemory constructs a [BoundedMemory] with the given options.
// The zero value `&BoundedMemory{}` is also valid and equivalent to
// `NewBoundedMemory()`; the single bounded default-slab pool defaults to
// [DefaultPoolCapacity].
func NewBoundedMemory(opts ...BoundedMemoryOption) *BoundedMemory {
	b := &BoundedMemory{}
	for _, o := range opts {
		o.applyBoundedMemory(b)
	}
	return b
}

// CompletionBuf returns a [Completion] slab of length n (defaulting to
// [DefaultCompletionBufSize] when [WithSize] is omitted). Requests up to the
// default size reuse a bounded pool of default-sized slabs and are sliced back
// to the requested length; larger requests allocate exact-size overflow slabs.
func (b *BoundedMemory) CompletionBuf(opts ...CompletionBufOption) []Completion {
	cfg := resolveCompletionBufConfig(opts, DefaultCompletionBufSize)
	if cfg.size <= DefaultCompletionBufSize {
		t := b.defaultPool()
		if idx, err := t.pool.Get(); err == nil {
			slab := t.pool.Value(idx)
			s := slab.data[:cfg.size]
			clearCompletions(s)
			return s
		}
		return make([]Completion, DefaultCompletionBufSize)[:cfg.size]
	}
	return make([]Completion, cfg.size)
}

// Release returns a default-sized slab to the bounded pool when it was
// originally allocated by this BoundedMemory; exact-size overflow slabs are
// dropped.
func (b *BoundedMemory) Release(buf []Completion) {
	if cap(buf) != DefaultCompletionBufSize {
		return
	}
	t := &b.pool
	if t.pool == nil {
		return
	}
	full := buf[:cap(buf)]
	key := unsafe.Pointer(unsafe.SliceData(full))
	slot, ok := t.bySlab[key]
	if !ok {
		return
	}
	clearCompletions(slot.data)
	_ = t.pool.Put(slot.idx)
}

func (b *BoundedMemory) defaultPool() *boundedPoolState {
	t := &b.pool
	b.initOnce.Do(func() {
		capacity := b.poolCapacity
		if capacity < 1 {
			capacity = DefaultPoolCapacity
		}
		pool := iobuf.NewBoundedPool[*poolSlab](capacity)
		actualCap := pool.Cap()
		slabs := make([]*poolSlab, actualCap)
		bySlab := make(map[unsafe.Pointer]*poolSlab, actualCap)
		// Keep the default slabs in one contiguous backing block so steady-
		// state access stays cache-friendly.
		backing := make([]Completion, actualCap*DefaultCompletionBufSize)
		for i := range slabs {
			start := i * DefaultCompletionBufSize
			end := start + DefaultCompletionBufSize
			s := &poolSlab{idx: i, data: backing[start:end:end]}
			slabs[i] = s
			bySlab[unsafe.Pointer(unsafe.SliceData(s.data))] = s
		}
		next := 0
		pool.Fill(func() *poolSlab {
			s := slabs[next]
			next++
			return s
		})
		pool.SetNonblock(true)
		t.pool = pool
		t.bySlab = bySlab
		t.backing = backing
	})
	return t
}

// clearCompletions zeros a recycled slab so stale pointer-bearing Completion
// values cannot be observed by the next consumer (and cannot pin objects
// alive across a pool round-trip).
func clearCompletions(s []Completion) {
	clear(s)
}
