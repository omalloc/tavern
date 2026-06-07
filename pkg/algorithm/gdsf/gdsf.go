package gdsf

import (
	"sort"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
)

type Cache[K comparable, V any] struct {
	UpperBound      int
	LowerBound      int
	clock           float64
	mu              sync.RWMutex
	entries         map[K]*entry[K, V]
	heap            []*entry[K, V]
	EvictionChannel chan<- storage.Eviction[K, V]
}

type entry[K comparable, V any] struct {
	key      K
	value    V
	size     int64
	freq     int
	priority float64
	index    int
}

func New[K comparable, V any](cap int) *Cache[K, V] {
	c := &Cache[K, V]{
		entries: make(map[K]*entry[K, V]),
	}
	if cap > 0 {
		c.UpperBound = cap
		c.LowerBound = cap
	}
	return c
}

func (c *Cache[K, V]) SetEvictionCh(ch chan<- storage.Eviction[K, V]) {
	c.EvictionChannel = ch
}

func (c *Cache[K, V]) Has(key K) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.entries[key]
	return ok
}

func (c *Cache[K, V]) Get(key K) *V {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.freq++
		e.priority = c.computePriority(e.freq, e.size)
		c.heapFix(e.index)
		return &e.value
	}
	return nil
}

func (c *Cache[K, V]) Peek(key K) *V {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.entries[key]; ok {
		return &e.value
	}
	return nil
}

func (c *Cache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.value = value
		e.freq++
		e.priority = c.computePriority(e.freq, e.size)
		c.heapFix(e.index)
	} else {
		e := &entry[K, V]{
			key:   key,
			value: value,
			size:  1,
			freq:  1,
		}
		e.priority = c.computePriority(e.freq, e.size)
		c.entries[key] = e
		c.heapPush(e)

		if c.UpperBound > 0 && c.LowerBound > 0 {
			if len(c.entries) > c.UpperBound {
				c.evict(len(c.entries) - c.LowerBound)
			}
		}
	}
}

func (c *Cache[K, V]) SetSize(key K, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return
	}
	if size <= 0 {
		size = 1
	}
	e.size = size
	e.priority = c.computePriority(e.freq, e.size)
	c.heapFix(e.index)
}

func (c *Cache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

func (c *Cache[K, V]) GetFrequency(key K) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.entries[key]; ok {
		return e.freq
	}
	return 0
}

func (c *Cache[K, V]) Keys() []K {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]K, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	return keys
}

func (c *Cache[K, V]) TopK(k int) []K {
	c.mu.RLock()
	defer c.mu.RUnlock()

	n := len(c.entries)
	if k > n {
		k = n
	}

	type kv struct {
		key      K
		priority float64
	}
	items := make([]kv, 0, n)
	for _, e := range c.entries {
		items = append(items, kv{e.key, e.priority})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].priority > items[j].priority
	})

	keys := make([]K, k)
	for i := range k {
		keys[i] = items[i].key
	}
	return keys
}

func (c *Cache[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		delete(c.entries, key)
		c.heapRemove(e.index)
		return true
	}
	return false
}

func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[K]*entry[K, V])
	c.heap = c.heap[:0]
	c.clock = 0
}

func (c *Cache[K, V]) Evict(count int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evict(count)
}

// computePriority computes the GDSF priority: H = clock + freq * (1 / size).
func (c *Cache[K, V]) computePriority(freq int, size int64) float64 {
	return c.clock + float64(freq)*(1.0/float64(size))
}

func (c *Cache[K, V]) evict(count int) int {
	var evicted int
	for evicted < count && len(c.heap) > 0 {
		e := c.heapPop()
		c.clock = e.priority

		if c.EvictionChannel != nil {
			select {
			case c.EvictionChannel <- storage.Eviction[K, V]{Key: e.key, Value: e.value}:
			default:
			}
		}
		delete(c.entries, e.key)
		evicted++
	}
	return evicted
}

// --- heap operations ---

func (c *Cache[K, V]) heapPush(e *entry[K, V]) {
	e.index = len(c.heap)
	c.heap = append(c.heap, e)
	c.heapUp(e.index)
}

func (c *Cache[K, V]) heapPop() *entry[K, V] {
	n := len(c.heap) - 1
	c.heapSwap(0, n)
	e := c.heap[n]
	c.heap = c.heap[:n]
	if n > 0 {
		c.heapDown(0)
	}
	return e
}

func (c *Cache[K, V]) heapFix(i int) {
	if i < 0 || i >= len(c.heap) {
		return
	}
	c.heapDown(i)
	c.heapUp(i)
}

func (c *Cache[K, V]) heapRemove(i int) {
	n := len(c.heap) - 1
	if i != n {
		c.heapSwap(i, n)
		c.heap = c.heap[:n]
		c.heapFix(i)
	} else {
		c.heap = c.heap[:n]
	}
}

func (c *Cache[K, V]) heapUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if c.heap[i].priority >= c.heap[parent].priority {
			break
		}
		c.heapSwap(i, parent)
		i = parent
	}
}

func (c *Cache[K, V]) heapDown(i int) {
	n := len(c.heap)
	for {
		smallest := i
		left := 2*i + 1
		right := 2*i + 2

		if left < n && c.heap[left].priority < c.heap[smallest].priority {
			smallest = left
		}
		if right < n && c.heap[right].priority < c.heap[smallest].priority {
			smallest = right
		}
		if smallest == i {
			break
		}
		c.heapSwap(i, smallest)
		i = smallest
	}
}

func (c *Cache[K, V]) heapSwap(i, j int) {
	c.heap[i], c.heap[j] = c.heap[j], c.heap[i]
	c.heap[i].index = i
	c.heap[j].index = j
}
