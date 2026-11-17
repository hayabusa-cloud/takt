// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt

const tableBucketBits = 10

// table reserves records before external acceptance. Index chains share those
// records, so all-colliding identities cannot exhaust separate index space.
// The serialized owner supplies the hash; lookup is O(capacity) in the worst case.
type table[K comparable, V any] struct {
	buckets     [1 << tableBucketBits]int
	entries     []entry[K, V]
	free, count int
}

type entry[K comparable, V any] struct {
	key   K
	value V
	next  int
	used  bool
}

// Use high product bits so aligned tokens do not all select the same bucket.
// This partitions identity traffic; equality still decides identity and every
// chain may use the full record arena. Collisions retain an O(capacity) bound.
func bucketIndex(hash uint64) uint64 { return (hash * 0x9e3779b97f4a7c15) >> (64 - tableBucketBits) }

func newTable[K comparable, V any](capacity int) table[K, V] {
	t := table[K, V]{entries: make([]entry[K, V], capacity)}
	for i := range t.buckets {
		t.buckets[i] = -1
	}
	for i := range t.entries {
		t.entries[i].next = i + 1
	}
	t.entries[capacity-1].next = -1
	return t
}

func (t *table[K, V]) reserve() int {
	i := t.free
	if i == -1 {
		return -1
	}
	t.free = t.entries[i].next
	t.entries[i].next = -1
	t.entries[i].used = true
	t.count++
	return i
}

// release requires removal from the identity index first.
func (t *table[K, V]) release(i int) {
	t.entries[i] = entry[K, V]{next: t.free}
	t.free = i
	t.count--
}

func (t *table[K, V]) lookup(key K, hash uint64) int {
	for i := t.buckets[bucketIndex(hash)]; i != -1; i = t.entries[i].next {
		if t.entries[i].key == key {
			return i
		}
	}
	return -1
}

func (t *table[K, V]) insert(i int, key K, hash uint64) bool {
	if t.lookup(key, hash) != -1 {
		return false
	}
	b := bucketIndex(hash)
	t.entries[i].key = key
	t.entries[i].next = t.buckets[b]
	t.buckets[b] = i
	return true
}

func (t *table[K, V]) remove(key K, hash uint64) int {
	link := &t.buckets[bucketIndex(hash)]
	for *link != -1 {
		i := *link
		if t.entries[i].key == key {
			*link = t.entries[i].next
			t.entries[i].next = -1
			return i
		}
		link = &t.entries[i].next
	}
	return -1
}

type queue struct {
	slots      []int
	head, size int
}

func newQueue(capacity int) queue {
	return queue{slots: make([]int, capacity)}
}

func (q *queue) push(i int) {
	tail := q.head + q.size
	if tail >= len(q.slots) {
		tail -= len(q.slots)
	}
	q.slots[tail] = i
	q.size++
}

func (q *queue) pop() int {
	i := q.slots[q.head]
	q.head++
	if q.head == len(q.slots) {
		q.head = 0
	}
	q.size--
	return i
}
