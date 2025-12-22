package memory

import (
	"context"
	"io/fs"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/algorithm/lru"
	"github.com/omalloc/tavern/storage/indexdb"
)

var _ storage.Bucket = (*memoryBucket)(nil)

type memoryBucket struct {
	fs        vfs.FS
	path      string
	dbPath    string
	driver    string
	storeType string
	weight    int
	sharedkv  storage.SharedKV
	indexdb   storage.IndexDB
	cache     *lru.Cache[object.IDHash, storage.Mark]
	fileMode  fs.FileMode
	maxSize   uint64
	stop      chan struct{}
}

func New(config *conf.Bucket, sharedkv storage.SharedKV) (storage.Bucket, error) {
	mb := &memoryBucket{
		fs:        vfs.NewMem(),
		path:      config.Path,
		dbPath:    indexdb.TypeInMemory,
		driver:    config.Driver,
		storeType: "memory",
		weight:    100, // default weight
		sharedkv:  sharedkv,
		cache:     lru.New[object.IDHash, storage.Mark](10_000), // in-memory object size
		fileMode:  fs.FileMode(0o755),
		maxSize:   1024 * 1024 * 100, // e.g. 100 MB
		stop:      make(chan struct{}, 1),
	}

	// create indexdb only in-memory
	db, err := indexdb.Create(config.DBType, indexdb.NewOption(mb.dbPath, indexdb.WithType("pebble")))
	if err != nil {
		log.Errorf("failed to create %s indexdb %v", config.DBType, err)
		return nil, err
	}
	mb.indexdb = db
	return mb, nil
}

// Allow implements [storage.Bucket].
func (m *memoryBucket) Allow() int {
	return int(m.maxSize)
}

// Close implements [storage.Bucket].
func (m *memoryBucket) Close() error {
	return m.indexdb.Close()
}

// Discard implements [storage.Bucket].
func (m *memoryBucket) Discard(ctx context.Context, id *object.ID) error {
	panic("unimplemented")
}

// DiscardWithHash implements [storage.Bucket].
func (m *memoryBucket) DiscardWithHash(ctx context.Context, hash object.IDHash) error {
	panic("unimplemented")
}

// DiscardWithMessage implements [storage.Bucket].
func (m *memoryBucket) DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error {
	panic("unimplemented")
}

// DiscardWithMetadata implements [storage.Bucket].
func (m *memoryBucket) DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error {
	panic("unimplemented")
}

// Exist implements [storage.Bucket].
func (m *memoryBucket) Exist(ctx context.Context, id object.IDHash) bool {
	return m.cache.Has(id)
}

// Expired implements [storage.Bucket].
func (m *memoryBucket) Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool {
	panic("unimplemented")
}

// HasBad implements [storage.Bucket].
func (m *memoryBucket) HasBad() bool {
	return false
}

// ID implements [storage.Bucket].
func (m *memoryBucket) ID() string {
	return m.path
}

// Iterate implements [storage.Bucket].
func (m *memoryBucket) Iterate(ctx context.Context, fn func(*object.Metadata) error) error {
	panic("unimplemented")
}

// Lookup implements [storage.Bucket].
func (m *memoryBucket) Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	return nil, storage.ErrKeyNotFound
}

// Path implements [storage.Bucket].
func (m *memoryBucket) Path() string {
	return m.path
}

// Remove implements [storage.Bucket].
func (m *memoryBucket) Remove(ctx context.Context, id *object.ID) error {
	return nil
}

// Store implements [storage.Bucket].
func (m *memoryBucket) Store(ctx context.Context, meta *object.Metadata) error {
	return nil
}

// StoreType implements [storage.Bucket].
func (m *memoryBucket) StoreType() string {
	return m.storeType
}

// Type implements [storage.Bucket].
func (m *memoryBucket) Type() string {
	return m.driver
}

// UseAllow implements [storage.Bucket].
func (m *memoryBucket) UseAllow() bool {
	return true
}

// Weight implements [storage.Bucket].
func (m *memoryBucket) Weight() int {
	return m.weight
}
