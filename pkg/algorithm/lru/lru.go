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
	freqs           *list.List[V]
	len             int
	lock            *sync.Mutex
	EvictionChannel chan<- Eviction[K, V]
}

type cacheEntry[K comparable, V any] struct {
	key      K
	value    V
	freqNode *list.Element[V]
}

type listEntry[K comparable, V any] struct {
	entries map[*cacheEntry[K, V]]byte
	freq    int
}

func New[K comparable, V any](cap int) *Cache[K, V] {
	c := new(Cache[K, V])
	c.values = make(map[K]*cacheEntry[K, V])
	c.freqs = list.New[V]()
	c.lock = new(sync.Mutex)
	if cap > 0 {
		c.UpperBound = cap
		c.LowerBound = cap
	}
	return c
}

// Has checks if the cache contains the given key, without incrementing the frequency.
func (c *Cache[K, V]) Has(key K) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	_, has := c.values[key]
	return has
}

// Get retrieves the key's value if it exists, incrementing the frequency.
// It returns nil if there is no value for the given key.
func (c *Cache[K, V]) Get(key K) *V {
	c.lock.Lock()
	defer c.lock.Unlock()
	if e, ok := c.values[key]; ok {
		c.increment(e)
		return &e.value
	}
	return nil
}

// Set sets given key-value in the cache.
// If the key-value already exists, it increases the frequency.
func (c *Cache[K, V]) Set(key K, value V) {
	c.lock.Lock()
	defer c.lock.Unlock()
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
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.len
}

// GetFrequency returns the frequency count of the given key
func (c *Cache[K, V]) GetFrequency(key K) int {
	c.lock.Lock()
	defer c.lock.Unlock()
	if e, ok := c.values[key]; ok {
		return e.freqNode.Value.(*listEntry[K, V]).freq
	}

	return 0
}

// Keys returns all the keys in the cache
func (c *Cache[K, V]) Keys() []K {
	keys := make([]K, len(c.values))
	i := 0
	for k := range c.values {
		keys[i] = k
		i++
	}

	return keys
}

func (c *Cache[K, V]) Evict(count int) int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.evict(count)
}

func (c *Cache[K, V]) evict(count int) int {
	// No lock here so it can be called
	// from within the lock (during Set)
	var evicted int
	for place := c.freqs.Front(); place != nil && evicted < count; place = c.freqs.Front() {
		entries := place.Value.(*listEntry[K, V]).entries
		for entry := range entries {
			if evicted >= count {
				break
			}
			if c.EvictionChannel != nil {
				c.EvictionChannel <- Eviction[K, V]{
					Key:   entry.key,
					Value: entry.value,
				}
			}
			delete(c.values, entry.key)
			c.remEntry(place, entry)
			evicted++
			c.len--
		}
	}
	return evicted
}

func (c *Cache[K, V]) increment(e *cacheEntry[K, V]) {
	currentPlace := e.freqNode
	var nextFreq int
	var nextPlace *list.Element[V]
	if currentPlace == nil {
		// new entry
		nextFreq = 1
		nextPlace = c.freqs.Front()
	} else {
		// move up
		nextFreq = currentPlace.Value.(*listEntry[K, V]).freq + 1
		nextPlace = currentPlace.Next()
	}

	if nextPlace == nil || nextPlace.Value.(*listEntry[K, V]).freq != nextFreq {
		// create a new list entry
		li := new(listEntry[K, V])
		li.freq = nextFreq
		li.entries = make(map[*cacheEntry[K, V]]byte)
		if currentPlace != nil {
			nextPlace = c.freqs.InsertAfter(li, currentPlace)
		} else {
			nextPlace = c.freqs.PushFront(li)
		}
	}
	e.freqNode = nextPlace
	nextPlace.Value.(*listEntry[K, V]).entries[e] = 1
	if currentPlace != nil {
		// remove from current position
		c.remEntry(currentPlace, e)
	}
}

func (c *Cache[K, V]) remEntry(place *list.Element[V], entry *cacheEntry[K, V]) {
	entries := place.Value.(*listEntry[K, V]).entries
	delete(entries, entry)
	if len(entries) == 0 {
		c.freqs.Remove(place)
	}
}
