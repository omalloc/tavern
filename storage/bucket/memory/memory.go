package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/algorithm/lru"
	"github.com/omalloc/tavern/pkg/iobuf"
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
	migration storage.Migration
	cache     *lru.Cache[object.IDHash, storage.Mark]
	fileFlag  int
	fileMode  fs.FileMode
	maxSize   uint64
	closed    bool
	stop      chan struct{}
}

func New(config *conf.Bucket, sharedkv storage.SharedKV) (storage.Bucket, error) {
	mb := &memoryBucket{
		fs:        vfs.NewMem(),
		path:      "/",
		dbPath:    storage.TypeInMemory,
		driver:    config.Driver,
		storeType: storage.TypeInMemory,
		weight:    100, // default weight
		sharedkv:  sharedkv,
		cache:     lru.New[object.IDHash, storage.Mark](config.MaxObjectLimit), // in-memory object size
		fileFlag:  os.O_RDONLY,
		fileMode:  fs.FileMode(0o755),
		maxSize:   1024 * 1024 * 100, // e.g. 100 MB
		stop:      make(chan struct{}, 1),
	}

	// create indexdb only in-memory
	db, err := indexdb.Create("pebble", indexdb.NewOption(mb.dbPath, indexdb.WithType("pebble")))
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
	if m.closed {
		return nil
	}

	m.closed = true
	return m.indexdb.Close()
}

// Discard implements [storage.Bucket].
func (m *memoryBucket) Discard(ctx context.Context, id *object.ID) error {
	md, err := m.indexdb.Get(ctx, id.Bytes())
	if err != nil {
		return err
	}

	return m.discard(ctx, md)
}

func (m *memoryBucket) discard(ctx context.Context, md *object.Metadata) error {
	// 缓存不存在
	if md == nil {
		return os.ErrNotExist
	}

	clog := log.Context(ctx)

	// 先删除 db 中的数据, 避免被其他协程 HIT
	if err := m.indexdb.Delete(ctx, md.ID.Bytes()); err != nil {
		clog.Warnf("failed to delete metadata %s: %v", md.ID.WPath(m.path), err)
	}

	// 如果缓存为1级，则清除全部子缓存(vary)
	if md.IsVary() && len(md.VirtualKey) > 0 {
		for _, varyKey := range md.VirtualKey {
			oid := object.NewVirtualID(md.ID.Path(), varyKey)
			if strings.EqualFold(oid.HashStr(), md.ID.HashStr()) {
				clog.Warnf("discard %s but level1 id equal level2 id", md.ID.WPath(m.path))
				continue
			}
			// discard leveled cache (vary,chunked)
			_ = m.Discard(ctx, oid)
		}
	}

	// 删除所有 slice 缓存文件
	md.Chunks.Range(func(x uint32) {
		wpath := md.ID.WPathSlice(m.path, x)
		if err := m.fs.Remove(wpath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Context(ctx).Errorf("failed to remove cached slice file %s: %v", wpath, err)
		}
	})

	// 删除目录倒排索引
	_ = m.sharedkv.Delete(ctx, []byte(fmt.Sprintf("ix/%s/%s", m.ID(), md.ID.Key())))

	if u, err1 := url.Parse(md.ID.Path()); err1 == nil {
		_, _ = m.sharedkv.Decr(ctx, []byte(fmt.Sprintf("if/domain/%s", u.Host)), 1)
	}

	return nil
}

// DiscardWithHash implements [storage.Bucket].
func (m *memoryBucket) DiscardWithHash(ctx context.Context, hash object.IDHash) error {
	id := hash[:]
	wpath := hash.WPath(m.path)

	md, err := m.indexdb.Get(ctx, id)
	if err != nil {
		return err
	}

	if log.Enabled(log.LevelDebug) {
		log.Debugf("discard url=%s hash=%s ", md.ID.Key(), wpath)
	}

	return m.discard(ctx, md)
}

// DiscardWithMessage implements [storage.Bucket].
func (m *memoryBucket) DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error {
	log.Context(ctx).Infof("discard %s [path=%s] with message %s", id, id.WPath(m.path), msg)
	return m.Discard(ctx, id)
}

// DiscardWithMetadata implements [storage.Bucket].
func (m *memoryBucket) DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error {
	return m.Discard(ctx, meta.ID)
}

// Exist implements [storage.Bucket].
func (m *memoryBucket) Exist(ctx context.Context, id []byte) bool {
	return m.cache.Has(object.IDHash(id))
}

func (m *memoryBucket) Objects() uint64 {
	return uint64(m.cache.Len())
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
	return m.indexdb.Iterate(ctx, nil, func(key []byte, val *object.Metadata) bool {
		return fn(val) == nil
	})
}

// Lookup implements [storage.Bucket].
func (m *memoryBucket) Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	md, err := m.indexdb.Get(ctx, id.Bytes())
	return md, err
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
	if log.Enabled(log.LevelDebug) {
		clog := log.Context(ctx)

		now := time.Now()
		defer func() {
			cost := time.Since(now)

			clog.Debugf("store metadata %s, cost %s", meta.ID.WPath(m.path), cost)
		}()
	}

	// before save metadata
	if err := m.indexdb.Set(ctx, meta.ID.Bytes(), meta); err != nil {
		return err
	}

	// update lru
	m.cache.Set(meta.ID.Hash(), storage.NewMark(meta.LastRefUnix, uint64(meta.Refs)))

	// save domains counter
	if u, err1 := url.Parse(meta.ID.Path()); err1 == nil {
		if _, err1 = m.sharedkv.Incr(context.Background(), []byte(fmt.Sprintf("if/domain/%s", u.Host)), 1); err1 != nil {
			log.Warnf("save kvstore domain %s failed", u.Host)
		}
	}
	// save directory tree index
	if err := m.sharedkv.Set(ctx, []byte(fmt.Sprintf("ix/%s/%s", m.ID(), meta.ID.Key())), meta.ID.Bytes()); err != nil {
		// ignore sharedkv error to not affect main storage
		_ = err
	}

	return nil
}

func (m *memoryBucket) WriteChunkFile(ctx context.Context, id *object.ID, index uint32) (io.WriteCloser, string, error) {
	wpath := id.WPathSlice(m.path, index)
	_ = m.fs.MkdirAll(filepath.Dir(wpath), m.fileMode)

	if log.Enabled(log.LevelDebug) {
		log.Context(ctx).Infof("write inmemory chunk file %s", wpath)
	}

	f, err := m.fs.OpenReadWrite(wpath, vfs.WriteCategoryUnspecified)
	if err != nil {
		return nil, wpath, err
	}

	return iobuf.ChunkWriterCloser(f, _empty), wpath, nil
}

func (m *memoryBucket) ReadChunkFile(_ context.Context, id *object.ID, index uint32) (storage.File, string, error) {
	wpath := id.WPathSlice(m.path, index)
	f, err := m.fs.Open(wpath)
	if err != nil {
		return nil, wpath, err
	}
	return storage.WrapVFSFile(f), wpath, err
}

// Migrate implements [storage.Bucket].
func (m *memoryBucket) Migrate(ctx context.Context, id *object.ID, dest storage.Bucket) error {
	panic("unimplemented")
}

// SetMigration implements [storage.Bucket].
func (m *memoryBucket) SetMigration(migration storage.Migration) error {
	m.migration = migration
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

func _empty() error {
	return nil
}
