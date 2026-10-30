package caching

import (
	"context"
	"sync"
)

// keyedGate coalesces work through publication/recheck, without sharing response
// bodies. Private/Vary:* outcomes remain exclusive to the requesting caller.
type keyedGate struct {
	mu      sync.Mutex
	entries map[string]*gateEntry
}
type gateEntry struct {
	token chan struct{}
	refs  int
}

func (g *keyedGate) lock(ctx context.Context, key string) (func(), error) {
	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[string]*gateEntry)
	}
	entry := g.entries[key]
	if entry == nil {
		entry = &gateEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		g.entries[key] = entry
	}
	entry.refs++
	g.mu.Unlock()
	releaseRef := func() {
		g.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(g.entries, key)
		}
		g.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	case <-entry.token:
	}
	var once sync.Once
	return func() { once.Do(func() { entry.token <- struct{}{}; releaseRef() }) }, nil
}
