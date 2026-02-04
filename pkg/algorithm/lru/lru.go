package lru

import (
	"sync"

	"github.com/omalloc/tavern/contrib/container/list"
)

type Eviction[K comparable, V any] struct {
	Key   K
	Value V
}

type Cache[K comparable, V any] struct {
	// If len > UpperBound, cache will automatically evict
	// down to LowerBound.  If either value is 0, this behavior
	// is disabled.
	UpperBound      int
	LowerBound      int
	values          map[K]*cacheEntry[K, V]
	freqs           *list.List[*listEntry[K, V]]
	len             int
	mu              sync.RWMutex
	EvictionChannel chan<- Eviction[K, V]
}

type cacheEntry[K comparable, V any] struct {
	key       K
	value     V
	freqNode  *list.Element[*listEntry[K, V]]
	entryNode *list.Element[*cacheEntry[K, V]]
}

type listEntry[K comparable, V any] struct {
	entries *list.List[*cacheEntry[K, V]]
	freq    int
}

func New[K comparable, V any](cap int) *Cache[K, V] {
	c := new(Cache[K, V])
	c.values = make(map[K]*cacheEntry[K, V])
	c.freqs = list.New[*listEntry[K, V]]()
	if cap > 0 {
		c.UpperBound = cap
		c.LowerBound = cap
	}
	return c
}

// Has checks if the cache contains the given key, without incrementing the frequency.
func (c *Cache[K, V]) Has(key K) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, has := c.values[key]
	return has
}

// Get retrieves the key's value if it exists, incrementing the frequency.
// It returns nil if there is no value for the given key.
func (c *Cache[K, V]) Get(key K) *V {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.values[key]; ok {
		c.increment(e)
		return &e.value
	}
	return nil
}

// Peek retrieves the key's value if it exists, without incrementing the frequency.
// It returns nil if there is no value for the given key.
func (c *Cache[K, V]) Peek(key K) *V {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.values[key]; ok {
		return &e.value
	}
	return nil
}

// Set sets given key-value in the cache.
// If the key-value already exists, it increases the frequency.
func (c *Cache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.values[key]; ok {
		// value already exists for key.  overwrite
		e.value = value
		c.increment(e)
	} else {
		// value doesn't exist.  insert
		e := new(cacheEntry[K, V])
		e.key = key
		e.value = value
		c.values[key] = e
		c.increment(e)
		c.len++
		// bounds mgmt
		if c.UpperBound > 0 && c.LowerBound > 0 {
			if c.len > c.UpperBound {
				c.evict(c.len - c.LowerBound)
			}
		}
	}
}

// Len returns the length of the cache
func (c *Cache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.len
}

// GetFrequency returns the frequency count of the given key
func (c *Cache[K, V]) GetFrequency(key K) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.values[key]; ok {
		return e.freqNode.Value.(*listEntry[K, V]).freq
	}

	return 0
}

// Keys returns all the keys in the cache
func (c *Cache[K, V]) Keys() []K {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]K, len(c.values))
	i := 0
	for k := range c.values {
		keys[i] = k
		i++
	}

	return keys
}

// TopK returns the top k most frequently used keys
func (c *Cache[K, V]) TopK(k int) []K {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]K, 0, k)
	// Iterate from highest frequency (Back) to lowest (Front)
	for freqNode := c.freqs.Back(); freqNode != nil; freqNode = freqNode.Prev() {
		li := freqNode.Value.(*listEntry[K, V])
		// Inside bucket, most recently added/promoted are at Back?
		// increment() uses PushBack.
		// So Back are the newest in this frequency.
		// We iterate from Back to Front to resolve ties with recency if needed,
		// or just simply collect them.
		for entryNode := li.entries.Back(); entryNode != nil; entryNode = entryNode.Prev() {
			entry := entryNode.Value.(*cacheEntry[K, V])
			keys = append(keys, entry.key)
			if len(keys) >= k {
				return keys
			}
		}
	}
	return keys
}

// Remove removes the given key from the cache.
func (c *Cache[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.values[key]; ok {
		delete(c.values, key)
		c.remEntry(e.freqNode, e)
		c.len--
		return true
	}
	return false
}

// Purge clears the cache.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values = make(map[K]*cacheEntry[K, V])
	c.freqs.Init()
	c.len = 0
}

func (c *Cache[K, V]) Evict(count int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evict(count)
}

func (c *Cache[K, V]) evict(count int) int {
	// No lock here so it can be called
	// from within the lock (during Set)
	var evicted int
	for place := c.freqs.Front(); place != nil && evicted < count; {
		li := place.Value.(*listEntry[K, V])
		for entryNode := li.entries.Front(); entryNode != nil && evicted < count; {
			entry := entryNode.Value.(*cacheEntry[K, V])
			if c.EvictionChannel != nil {
				select {
				case c.EvictionChannel <- Eviction[K, V]{
					Key:   entry.key,
					Value: entry.value,
				}:
				default:
				}
			}
			delete(c.values, entry.key)
			// Move to next before removing
			nextEntryNode := entryNode.Next()
			c.remEntry(place, entry)
			entryNode = nextEntryNode
			evicted++
			c.len--
		}
		// If the entire freq bucket was emptied, place.Next() might be nil or changed.
		// remEntry handles removing the bucket from c.freqs if it's empty.
		// So we always get Front() again if we still need to evict.
		place = c.freqs.Front()
	}
	return evicted
}

func (c *Cache[K, V]) increment(e *cacheEntry[K, V]) {
	currentPlace := e.freqNode
	var nextFreq int
	var nextPlace *list.Element[*listEntry[K, V]]
	if currentPlace == nil {
		// new entry
		nextFreq = 1
		nextPlace = c.freqs.Front()
	} else {
		// move up
		nextFreq = currentPlace.Value.(*listEntry[K, V]).freq + 1
		nextPlace = currentPlace.Next()
	}

	var oldEntryNode *list.Element[*cacheEntry[K, V]]
	if currentPlace != nil {
		oldEntryNode = e.entryNode
	}

	if nextPlace == nil || nextPlace.Value.(*listEntry[K, V]).freq != nextFreq {
		// create a new list entry
		li := new(listEntry[K, V])
		li.freq = nextFreq
		li.entries = list.New[*cacheEntry[K, V]]()
		if currentPlace != nil {
			nextPlace = c.freqs.InsertAfter(li, currentPlace)
		} else {
			nextPlace = c.freqs.PushFront(li)
		}
	}
	e.freqNode = nextPlace
	e.entryNode = nextPlace.Value.(*listEntry[K, V]).entries.PushBack(e)
	if currentPlace != nil {
		// remove from current position
		li := currentPlace.Value.(*listEntry[K, V])
		li.entries.Remove(oldEntryNode)
		if li.entries.Len() == 0 {
			c.freqs.Remove(currentPlace)
		}
	}
}

func (c *Cache[K, V]) remEntry(place *list.Element[*listEntry[K, V]], entry *cacheEntry[K, V]) {
	li := place.Value.(*listEntry[K, V])
	li.entries.Remove(entry.entryNode)
	if li.entries.Len() == 0 {
		c.freqs.Remove(place)
	}
}
