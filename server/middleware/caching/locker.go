package caching

import (
	"sync"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
)

var _ storagev1.ResourceLocker = (*Caching)(nil)

var globalLocker = &resourceLocker{
	locks: make(map[string]*sync.RWMutex),
}

type resourceLocker struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

func (r *resourceLocker) getLock(key string) *sync.RWMutex {
	r.mu.Lock()
	defer r.mu.Unlock()

	if l, ok := r.locks[key]; ok {
		return l
	}

	l := &sync.RWMutex{}
	r.locks[key] = l
	return l
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

func (c *Caching) TryLock() bool {
	if c.id == nil {
		return false
	}
	return globalLocker.getLock(c.Key()).TryLock()
}
