package storage

import (
	"context"
	"errors"
	"io"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

type Selector interface {
	// Select selects the Bucket by the object ID.
	Select(ctx context.Context, id *object.ID) Bucket
	// Rebuild rebuilds the Bucket hashring.
	// do not call this method frequently.
	Rebuild(ctx context.Context, buckets []Bucket) error
}

type Operation interface {
	// Lookup retrieves the metadata for the specified object ID.
	Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error)
	// Store store the metadata for the specified object ID.
	Store(ctx context.Context, meta *object.Metadata) error
	// Exist checks if the object exists.
	Exist(ctx context.Context, id []byte) bool
	// Remove soft-removes the object.
	Remove(ctx context.Context, id *object.ID) error
	// Discard hard-removes the object.
	Discard(ctx context.Context, id *object.ID) error
	// DiscardWithHash hard-removes the hash of the object.
	DiscardWithHash(ctx context.Context, hash object.IDHash) error
	// DiscardWithMessage hard-removes the object with a message.
	DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error
	// DiscardWithMetadata hard-removes the object with a metadata.
	DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error
	// Iterate iterates the objects.
	Iterate(ctx context.Context, fn func(*object.Metadata) error) error
	// Expired if the object is expired callback.
	Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool
}

type Storage interface {
	io.Closer
	Selector

	Buckets() []Bucket

	SharedKV() SharedKV

	PURGE(storeUrl string, typ PurgeControl) error
}

type Bucket interface {
	io.Closer
	Operation

	// ID returns the Bucket ID.
	ID() string
	// Weight returns the Bucket weight, range 0-1000.
	Weight() int
	// Allow returns the allow percent of the Bucket, range 0-100.
	Allow() int
	// UseAllow returns whether to use the allow percent.
	UseAllow() bool
	// HasBad returns whether the Bucket is in bad state.
	HasBad() bool
	// Type returns the Bucket type, empty memory native
	Type() string
	// StoreType returns the Bucket store-type, cold hot fastmemory
	StoreType() string
	// Path returns the Bucket path.
	Path() string
}

type PurgeControl struct {
	Hard        bool `json:"hard"`         // 是否硬删除, default: false 与 MarkExpired 冲突
	Dir         bool `json:"dir"`          // 是否清理目录, default: false
	MarkExpired bool `json:"mark_expired"` // 是否标记为过期, default: false 与 Hard 冲突
}

var ErrSharedKVKeyNotFound = errors.New("key not found")

type SharedKV interface {
	io.Closer

	// Get returns the value for the given key.
	Get(ctx context.Context, key []byte) ([]byte, error)
	// Set sets the value for the given key.
	Set(ctx context.Context, key []byte, val []byte) error
	// Incr increments the value for the given key.
	Incr(ctx context.Context, key []byte, delta uint32) (uint32, error)
	// Decr decrements the value for the given key.
	Decr(ctx context.Context, key []byte, delta uint32) (uint32, error)
	// GetCounter returns the value for the given key.
	GetCounter(ctx context.Context, key []byte) (uint32, error)
	// Delete deletes the value for the given key.
	Delete(ctx context.Context, key []byte) error
	// DropPrefix deletes all key-value pairs with the given prefix.
	DropPrefix(ctx context.Context, prefix []byte) error
	// Iterate iterates over all key-value pairs.
	Iterate(ctx context.Context, f func(key, val []byte) error) error
	// IteratePrefix iterates over all key-value pairs with the given prefix.
	IteratePrefix(ctx context.Context, prefix []byte, f func(key, val []byte) error) error
}

type Mark uint64

const (
	ClockBits   = 48
	ClockMask   = (1 << ClockBits) - 1
	CounterBits = 16
	RefsMask    = (1 << CounterBits) - 1
)

func NewMark(clock int64, refs uint64) Mark {
	return Mark(clock)<<CounterBits | Mark(refs)
}

func (m *Mark) SetLastAccess(clock int64) {
	*m &^= ClockMask << CounterBits
	*m |= Mark(clock) << CounterBits
}

func (m *Mark) LastAccess() uint64 {
	return uint64(*m >> CounterBits)
}

func (m *Mark) Refs() uint64 {
	return uint64(*m & RefsMask)
}

func (m *Mark) SetRefs(refs uint64) {
	if refs > RefsMask {
		refs = RefsMask
	}

	*m &^= RefsMask
	*m |= Mark(refs)
}

type CacheStatus int

const (
	// CacheMiss indicates the absence of a cached resource.
	CacheMiss CacheStatus = iota + 1
	// CacheHit indicates the presence of a cached resource.
	CacheHit
	// CacheParentHit indicates the cache hit occurred in a parent cache layer.
	CacheParentHit
	// CachePartHit indicates a partial cache hit for a range request.
	CachePartHit
	// CacheRevalidateHit indicates a hit after cache validation with the origin server.
	CacheRevalidateHit
	// CacheRevalidateMiss indicates a miss after cache validation with the origin server.
	CacheRevalidateMiss
	// CachePartMiss indicates only non-range parts of a resource are cached, requiring origin fetch for range requests.
	CachePartMiss
	// CacheHotHit indicates a hit from a hot/fast-access cache layer.
	CacheHotHit
	// BYPASS indicates the request bypassed the cache entirely.
	BYPASS
)

var cacheStatusMap = map[CacheStatus]string{
	CacheMiss:           "MISS",
	CacheHit:            "HIT",
	CacheParentHit:      "PARENT_HIT",
	CacheRevalidateHit:  "REVALIDATE_HIT",
	CacheRevalidateMiss: "REVALIDATE_MISS",
	CachePartHit:        "PART_HIT",
	CachePartMiss:       "PART_MISS",
	CacheHotHit:         "HOT_HIT",
	BYPASS:              "BYPASS",
}

func (r CacheStatus) String() string {
	return cacheStatusMap[r]
}
