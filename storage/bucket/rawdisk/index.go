package rawdisk

import (
	"sync"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

const (
	NumShards = 256
)

// IndexEntry represents the metadata location on the raw disk.
type IndexEntry struct {
	Offset    uint64
	Size      uint32
	ExpiresAt int64
}

// IsExpired checks if the entry is expired.
func (e *IndexEntry) IsExpired() bool {
	if e.ExpiresAt == 0 {
		return false
	}
	return time.Now().Unix() > e.ExpiresAt
}

// IndexShard is a single shard of the concurrent hash map.
type IndexShard struct {
	mu sync.RWMutex
	m  map[object.IDHash]*IndexEntry
}

// IndexTable is a concurrent-friendly sharded hash map for memory indexing.
type IndexTable struct {
	shards [NumShards]*IndexShard
}

// NewIndexTable creates a new sharded IndexTable.
func NewIndexTable() *IndexTable {
	t := &IndexTable{}
	for i := 0; i < NumShards; i++ {
		t.shards[i] = &IndexShard{
			m: make(map[object.IDHash]*IndexEntry),
		}
	}
	return t
}

// getShard returns the specific shard for a given IDHash.
func (t *IndexTable) getShard(hash object.IDHash) *IndexShard {
	// Simple hash function using the first byte of IDHash to determine shard
	// In production, might want a better distribution if IDHash is not uniformly random
	shardIdx := uint32(hash[0]) % NumShards
	return t.shards[shardIdx]
}

// Set adds or updates an entry in the index table.
func (t *IndexTable) Set(hash object.IDHash, entry *IndexEntry) {
	shard := t.getShard(hash)
	shard.mu.Lock()
	shard.m[hash] = entry
	shard.mu.Unlock()
}

// Get retrieves an entry from the index table. Returns nil if not found.
func (t *IndexTable) Get(hash object.IDHash) *IndexEntry {
	shard := t.getShard(hash)
	shard.mu.RLock()
	entry, ok := shard.m[hash]
	shard.mu.RUnlock()
	if !ok {
		return nil
	}
	return entry
}

// Delete removes an entry from the index table.
func (t *IndexTable) Delete(hash object.IDHash) {
	shard := t.getShard(hash)
	shard.mu.Lock()
	delete(shard.m, hash)
	shard.mu.Unlock()
}

// Range calls the given function for each entry in the index table.
// If the function returns false, the iteration stops.
func (t *IndexTable) Range(f func(hash object.IDHash, entry *IndexEntry) bool) {
	for i := 0; i < NumShards; i++ {
		shard := t.shards[i]
		shard.mu.RLock()
		for k, v := range shard.m {
			if !f(k, v) {
				shard.mu.RUnlock()
				return
			}
		}
		shard.mu.RUnlock()
	}
}

// Count returns the total number of entries across all shards.
func (t *IndexTable) Count() int {
	var count int
	for i := 0; i < NumShards; i++ {
		shard := t.shards[i]
		shard.mu.RLock()
		count += len(shard.m)
		shard.mu.RUnlock()
	}
	return count
}
