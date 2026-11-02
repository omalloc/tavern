package caching

import (
	"hash/fnv"
	"sync"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
)

var _ storagev1.ResourceLocker = (*Caching)(nil)

var globalLocker = &resourceLocker{}

type resourceLocker struct {
	// Fixed stripes bound memory independently of URL cardinality. Hash
	// collisions only serialize unrelated objects; the same key always uses
	// the same lock, including while other callers are waiting for it.
	locks [256]sync.RWMutex
}

func (r *resourceLocker) getLock(key string) *sync.RWMutex {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return &r.locks[h.Sum64()%uint64(len(r.locks))]
}

// Key implements storage.ResourceLocker.
func (c *Caching) Key() string {
	return c.id.String()
}

// Lock implements storage.ResourceLocker.
func (c *Caching) Lock() {
	if c.id == nil {
		return
	}
	globalLocker.getLock(c.Key()).Lock()
}

// Unlock implements storage.ResourceLocker.
func (c *Caching) Unlock() {
	if c.id == nil {
		return
	}
	globalLocker.getLock(c.Key()).Unlock()
}

// RLock implements storage.ResourceLocker.
func (c *Caching) RLock() {
	if c.id == nil {
		return
	}
	globalLocker.getLock(c.Key()).RLock()
}

// RUnlock implements storage.ResourceLocker.
func (c *Caching) RUnlock() {
	if c.id == nil {
		return
	}
	globalLocker.getLock(c.Key()).RUnlock()
}
