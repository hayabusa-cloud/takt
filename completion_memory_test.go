// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"slices"
	"testing"
	"unsafe"

	"code.hybscloud.com/iobuf"
	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

func TestDefaultCompletionBufSize(t *testing.T) {
	want := iobuf.BufferSizeLarge / int(unsafe.Sizeof(takt.Completion{}))
	if takt.DefaultCompletionBufSize != want {
		t.Fatalf("DefaultCompletionBufSize=%d want %d", takt.DefaultCompletionBufSize, want)
	}
	if takt.DefaultCompletionBufBytes != iobuf.BufferSizeLarge {
		t.Fatalf("DefaultCompletionBufBytes=%d want %d",
			takt.DefaultCompletionBufBytes, iobuf.BufferSizeLarge)
	}
}

func TestHeapMemoryDefaultLength(t *testing.T) {
	var h takt.HeapMemory
	buf := h.CompletionBuf()
	if len(buf) != takt.DefaultCompletionBufSize {
		t.Fatalf("default len=%d want %d", len(buf), takt.DefaultCompletionBufSize)
	}
}

func TestHeapMemoryWithSize(t *testing.T) {
	var h takt.HeapMemory
	for _, n := range []int{1, 8, 64, 1024} {
		buf := h.CompletionBuf(takt.WithSize(n))
		if len(buf) != n {
			t.Fatalf("WithSize(%d): len=%d", n, len(buf))
		}
	}
}

func TestHeapMemoryPoolReuseAtDefaultSize(t *testing.T) {
	h := &takt.HeapMemory{}
	first := h.CompletionBuf()
	firstAddr := unsafe.SliceData(first)
	h.Release(first)
	// sync.Pool is a best-effort cache and may drop items under -race /
	// coverage instrumentation, so reuse is observed opportunistically rather
	// than required. The hard contract is that default-sized round-trips remain
	// valid and keep the visible length stable.
	reused := false
	for range 64 {
		s := h.CompletionBuf()
		if len(s) != takt.DefaultCompletionBufSize {
			t.Fatalf("default len=%d want %d", len(s), takt.DefaultCompletionBufSize)
		}
		if unsafe.SliceData(s) == firstAddr {
			reused = true
		}
		h.Release(s)
	}
	if !reused {
		t.Log("sync.Pool reuse not observed under current runtime; best-effort cache semantics")
	}
}

func TestHeapMemoryReleaseNonDefaultDropped(t *testing.T) {
	var h takt.HeapMemory
	odd := h.CompletionBuf(takt.WithSize(7))
	h.Release(odd) // dropped
	got := h.CompletionBuf(takt.WithSize(7))
	if len(got) != 7 {
		t.Fatalf("len=%d", len(got))
	}
}

func TestHeapMemoryReleaseClearsOnReuse(t *testing.T) {
	var h takt.HeapMemory
	buf := h.CompletionBuf()
	buf[0] = takt.Completion{Token: 42, Value: kont.Resumed("dirty")}
	h.Release(buf)
	again := h.CompletionBuf()
	var zero takt.Completion
	if again[0] != zero {
		t.Fatalf("recycled slab not cleared: %+v", again[0])
	}
	h.Release(again)
}

func TestBoundedMemoryDefaultPoolCapacity(t *testing.T) {
	var b takt.BoundedMemory
	for _, n := range []int{1, 8, 64, takt.DefaultCompletionBufSize} {
		buf := b.CompletionBuf(takt.WithSize(n))
		if len(buf) != n {
			t.Fatalf("len=%d want %d", len(buf), n)
		}
		if cap(buf) != takt.DefaultCompletionBufSize {
			t.Fatalf("n=%d cap=%d want %d", n, cap(buf), takt.DefaultCompletionBufSize)
		}
	}

	over := takt.DefaultCompletionBufSize + 1
	buf := b.CompletionBuf(takt.WithSize(over))
	if len(buf) != over {
		t.Fatalf("overflow len=%d want %d", len(buf), over)
	}
	if cap(buf) != over {
		t.Fatalf("overflow cap=%d want %d", cap(buf), over)
	}
}

func TestBoundedMemoryDefaultLength(t *testing.T) {
	var b takt.BoundedMemory
	buf := b.CompletionBuf()
	if len(buf) != takt.DefaultCompletionBufSize {
		t.Fatalf("default len=%d want %d", len(buf), takt.DefaultCompletionBufSize)
	}
}

func TestBoundedMemoryPoolReuseAtDefaultSize(t *testing.T) {
	b := takt.NewBoundedMemory()
	first := b.CompletionBuf(takt.WithSize(64))
	addr := unsafe.SliceData(first)
	b.Release(first)
	// BoundedPool is FIFO; the released default slab will be returned within
	// DefaultPoolCapacity rounds. Acquire and release one slab at a time so
	// the released slab returns to the head before the next Get.
	reused := false
	for range takt.DefaultPoolCapacity * 2 {
		s := b.CompletionBuf(takt.WithSize(64))
		if unsafe.SliceData(s) == addr {
			reused = true
		}
		b.Release(s)
	}
	if !reused {
		t.Fatalf("expected bounded pool to reuse the slab within capacity")
	}
}

func TestBoundedMemoryDefaultPoolUsesContiguousBacking(t *testing.T) {
	b := takt.NewBoundedMemory()
	bufs := make([][]takt.Completion, 0, takt.DefaultPoolCapacity)
	addrs := make([]uintptr, 0, takt.DefaultPoolCapacity)
	for range takt.DefaultPoolCapacity {
		buf := b.CompletionBuf()
		bufs = append(bufs, buf)
		addrs = append(addrs, uintptr(unsafe.Pointer(unsafe.SliceData(buf))))
	}
	slices.Sort(addrs)
	stride := uintptr(takt.DefaultCompletionBufSize) * unsafe.Sizeof(takt.Completion{})
	for i := 1; i < len(addrs); i++ {
		if got := addrs[i] - addrs[i-1]; got != stride {
			t.Fatalf("slab stride=%d want %d", got, stride)
		}
	}
	for _, buf := range bufs {
		b.Release(buf)
	}
}

func TestBoundedMemoryReleaseDropsNonDefaultShape(t *testing.T) {
	var b takt.BoundedMemory
	weird := make([]takt.Completion, 5) // cap=5 → not the default slab shape
	weirdAddr := unsafe.SliceData(weird)
	b.Release(weird) // dropped silently
	fresh := b.CompletionBuf(takt.WithSize(5))
	if unsafe.SliceData(fresh) == weirdAddr {
		t.Fatalf("BoundedMemory should not have reused the non-default release")
	}
}

func TestBoundedMemoryReleaseEmpty(t *testing.T) {
	var b takt.BoundedMemory
	b.Release(nil)
	b.Release([]takt.Completion{})
}

func TestBoundedMemoryReleaseClearsOnReuse(t *testing.T) {
	var b takt.BoundedMemory
	buf := b.CompletionBuf(takt.WithSize(64))
	buf[0] = takt.Completion{Token: 99, Value: kont.Resumed("dirty")}
	b.Release(buf)
	again := b.CompletionBuf(takt.WithSize(64))
	var zero takt.Completion
	if again[0] != zero {
		t.Fatalf("recycled bounded slab not cleared: %+v", again[0])
	}
	b.Release(again)
}

func TestNewLoopUsesHeapMemoryDefault(t *testing.T) {
	b := &observingBackend{}
	l := takt.NewLoop[*observingBackend, int](b)
	if _, err := l.Poll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.seenBufLen != takt.DefaultCompletionBufSize {
		t.Fatalf("default CompletionMemory: Poll saw buffer len %d, want %d",
			b.seenBufLen, takt.DefaultCompletionBufSize)
	}
}

func TestNewLoopWithMaxCompletionsRoutesSize(t *testing.T) {
	b := &observingBackend{}
	l := takt.NewLoop[*observingBackend, int](b, takt.WithMaxCompletions(8))
	if _, err := l.Poll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.seenBufLen != 8 {
		t.Fatalf("Poll saw buffer len %d, want 8", b.seenBufLen)
	}
}

func TestNewLoopWithMemoryRoutesProvider(t *testing.T) {
	mem := &recordingMemory{}
	b := &observingBackend{}
	l := takt.NewLoop[*observingBackend, int](b, takt.WithMemory(mem),
		takt.WithMaxCompletions(8))
	if mem.calls != 1 {
		t.Fatalf("memory calls=%d want 1", mem.calls)
	}
	if mem.lastN != 8 {
		t.Fatalf("memory size=%d want 8", mem.lastN)
	}
	if _, err := l.Poll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.seenBufLen != 8 {
		t.Fatalf("Poll saw buffer len %d, want 8", b.seenBufLen)
	}
	if mem.released {
		t.Fatalf("CompletionMemory.Release called before Drain")
	}
	l.Drain()
	if !mem.released {
		t.Fatalf("Drain did not Release the slab")
	}
}

func TestNewLoopWithBoundedMemoryEndToEnd(t *testing.T) {
	b := &observingBackend{}
	mem := &takt.BoundedMemory{}
	l := takt.NewLoop[*observingBackend, int](b, takt.WithMemory(mem),
		takt.WithMaxCompletions(8))
	if _, err := l.Poll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.seenBufLen != 8 {
		t.Fatalf("Poll saw buffer len %d, want 8", b.seenBufLen)
	}
	l.Drain()
}

func TestWithSizeRejectsNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		func(n int) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("WithSize(%d): expected panic", n)
				}
			}()
			_ = takt.WithSize(n)
		}(n)
	}
}

func TestWithMaxCompletionsRejectsNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = takt.WithMaxCompletions(0)
}

func TestWithMemoryRejectsNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = takt.WithMemory(nil)
}

type shortMemory struct{}

func (shortMemory) CompletionBuf(opts ...takt.CompletionBufOption) []takt.Completion {
	return make([]takt.Completion, 1)
}

func (shortMemory) Release([]takt.Completion) {}

type emptyDefaultMemory struct{}

func (emptyDefaultMemory) CompletionBuf(opts ...takt.CompletionBufOption) []takt.Completion {
	return nil
}

func (emptyDefaultMemory) Release([]takt.Completion) {}

type oversizedMemory struct{}

func (oversizedMemory) CompletionBuf(opts ...takt.CompletionBufOption) []takt.Completion {
	return make([]takt.Completion, 16)
}

func (oversizedMemory) Release([]takt.Completion) {}

func TestNewLoopRejectsShortCompletionBuffer(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		} else if msg, ok := r.(string); !ok || msg != "takt: CompletionMemory returned short completion buffer" {
			t.Fatalf("panic = %v, want fixed message", r)
		}
	}()
	_ = takt.NewLoop[*observingBackend, int](&observingBackend{},
		takt.WithMemory(shortMemory{}), takt.WithMaxCompletions(4))
}

func TestNewLoopRejectsEmptyDefaultCompletionBuffer(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		} else if msg, ok := r.(string); !ok || msg != "takt: CompletionMemory returned empty default completion buffer" {
			t.Fatalf("panic = %v, want fixed message", r)
		}
	}()
	_ = takt.NewLoop[*observingBackend, int](&observingBackend{},
		takt.WithMemory(emptyDefaultMemory{}))
}

func TestNewLoopCapsVisibleLengthOnOversizedProvider(t *testing.T) {
	b := &observingBackend{}
	l := takt.NewLoop[*observingBackend, int](b,
		takt.WithMemory(oversizedMemory{}), takt.WithMaxCompletions(8))
	if _, err := l.Poll(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.seenBufLen != 8 {
		t.Fatalf("Poll saw buffer len %d, want 8", b.seenBufLen)
	}
}

func TestDrainIsIdempotentOnMemory(t *testing.T) {
	mem := &recordingMemory{}
	b := &observingBackend{}
	l := takt.NewLoop[*observingBackend, int](b, takt.WithMemory(mem),
		takt.WithMaxCompletions(4))
	l.Drain()
	first := mem.releaseCalls
	l.Drain()
	if mem.releaseCalls != first {
		t.Fatalf("Drain Release not idempotent: %d → %d", first, mem.releaseCalls)
	}
}

func TestNewBoundedMemoryWithPoolCapacity(t *testing.T) {
	b := takt.NewBoundedMemory(takt.WithPoolCapacity(4))
	// Drain the default bounded slab pool to verify the cap actually applied
	// (pool.Cap rounds up to a power of two, so 4 stays 4). Ask for slabs until
	// we get one that is NOT pointer-equal to any earlier one beyond capacity
	// rounds.
	bufs := make([][]takt.Completion, 0, 8)
	addrs := make(map[uintptr]struct{})
	for range 4 {
		s := b.CompletionBuf(takt.WithSize(64))
		addrs[uintptr(unsafe.Pointer(unsafe.SliceData(s)))] = struct{}{}
		bufs = append(bufs, s)
	}
	if len(addrs) != 4 {
		t.Fatalf("expected 4 distinct pooled slabs, got %d", len(addrs))
	}
	// 5th allocation: pool empty, falls back to an off-pool fresh slab.
	overflow := b.CompletionBuf(takt.WithSize(64))
	overflowAddr := uintptr(unsafe.Pointer(unsafe.SliceData(overflow)))
	if _, ok := addrs[overflowAddr]; ok {
		t.Fatalf("overflow slab should not equal any pooled slab")
	}
	// Releasing the overflow slab is a drop (not in bySlab).
	b.Release(overflow)
	for _, s := range bufs {
		b.Release(s)
	}
}

func TestWithPoolCapacityRejectsNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = takt.WithPoolCapacity(0)
}

func TestBoundedMemoryReleaseBeforeInitialization(t *testing.T) {
	b := takt.NewBoundedMemory()
	// No CompletionBuf call yet; releasing a synthetic default-sized slab must
	// be a no-op (no panic).
	fake := make([]takt.Completion, takt.DefaultCompletionBufSize)
	b.Release(fake)
}

func TestBoundedMemoryDropsForeignSlab(t *testing.T) {
	b := takt.NewBoundedMemory(takt.WithPoolCapacity(2))
	// Force pool init by acquiring (and releasing) one slab.
	owned := b.CompletionBuf(takt.WithSize(64))
	b.Release(owned)
	// Now release a synthetic same-shape slab not allocated by b → drop.
	foreign := make([]takt.Completion, cap(owned))
	b.Release(foreign[:cap(foreign)])
}
