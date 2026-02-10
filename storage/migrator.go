package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/storage/bucket/empty"
	"github.com/omalloc/tavern/storage/selector"
	"github.com/omalloc/tavern/storage/sharedkv"
)

var _ storage.Migrator = (*migratorStorage)(nil)

type migratorStorage struct {
	closed bool
	mu     sync.Mutex
	log    *log.Helper

	warmSelector storage.Selector // warm selector
	hotSelector  storage.Selector // hot selector
	coldSelector storage.Selector // cold selector
	sharedkv     storage.SharedKV
	nopBucket    storage.Bucket
	memoryBucket storage.Bucket
	hotBucket    []storage.Bucket
	warmBucket   []storage.Bucket
	coldBucket   []storage.Bucket
}

func NewMigrator(config *conf.Storage, logger log.Logger) (storage.Migrator, error) {
	nopBucket, _ := empty.New(&storage.BucketConfig{}, sharedkv.NewEmpty())

	m := &migratorStorage{
		closed: false,
		mu:     sync.Mutex{},
		log:    log.NewHelper(logger),

		warmSelector: nil,
		hotSelector:  nil,
		coldSelector: nil,
		sharedkv:     sharedkv.NewMemSharedKV(),
		nopBucket:    nopBucket,
		memoryBucket: nil,
		hotBucket:    make([]storage.Bucket, 0, len(config.Buckets)),
		warmBucket:   make([]storage.Bucket, 0, len(config.Buckets)),
		coldBucket:   make([]storage.Bucket, 0, len(config.Buckets)),
	}

	if err := m.reinit(config); err != nil {
		return nil, err
	}

	// diraware adapter
	// 关闭可以提升性能，但是目录推送只能使用硬删除模式，无法使用过期标记
	if config.DirAware != nil && config.DirAware.Enabled {
		if config.DirAware.StorePath != "" {
			_ = os.MkdirAll(config.DirAware.StorePath, 0755)
			// replace memkv with storekv
			m.sharedkv = sharedkv.NewStoreSharedKV(config.DirAware.StorePath)
		}

		// sharedkv used no-mem typ.
		// return diraware.New(m, diraware.NewChecker(m.sharedkv,
		// 	diraware.WithAutoClear(config.DirAware.AutoClear),
		// )), nil
	}

	return m, nil
}

func (m *migratorStorage) reinit(config *conf.Storage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := m.sharedkv.DropPrefix(ctx, []byte("if/domain/")); err != nil {
		m.log.Warnf("failed to drop prefix key `if/domain/` counter: %s", err)
	}

	globalConfig := &globalBucketOption{
		AsyncLoad:       config.AsyncLoad,
		EvictionPolicy:  config.EvictionPolicy,
		SelectionPolicy: config.SelectionPolicy,
		Driver:          config.Driver,
		DBType:          config.DBType,
		DBPath:          config.DBPath,
		Migration: &storage.MigrationConfig{
			Enabled: config.Migration.Enabled,
			Promote: storage.PromoteConfig{
				MinHits: config.Migration.Promote.MinHits,
				Window:  config.Migration.Promote.Window,
			},
			Demote: storage.DemoteConfig{
				MinHits: config.Migration.Demote.MinHits,
				Window:  config.Migration.Demote.Window,
			},
		},
	}

	for _, c := range config.Buckets {
		bucket, err := NewBucket(mergeConfig(globalConfig, c), m.sharedkv)
		if err != nil {
			return err
		}

		switch bucket.StoreType() {
		case storage.TypeNormal, storage.TypeWarm:
			m.warmBucket = append(m.warmBucket, bucket)
		case storage.TypeHot:
			m.hotBucket = append(m.hotBucket, bucket)
		case storage.TypeCold:
			m.coldBucket = append(m.coldBucket, bucket)
		case storage.TypeInMemory:
			if m.memoryBucket != nil {
				return fmt.Errorf("only one inmemory bucket is allowed")
			}
			m.memoryBucket = bucket
		}
	}

	// wait for all buckets to be initialized
	// load indexdb
	// load lru
	// load purge queue

	// storage layer init.
	// hot
	if len(m.hotBucket) > 0 {
		m.hotSelector = selector.New(m.hotBucket, config.SelectionPolicy)
	} else {
		m.log.Infof("no hot bucket configured")
	}

	// warm
	if len(m.warmBucket) <= 0 {
		m.log.Infof("no warm bucket configured")
		// no warm bucket, use memory bucket
		if m.memoryBucket != nil {
			m.warmBucket = append(m.warmBucket, m.memoryBucket)
		}
	}
	m.warmSelector = selector.New(m.warmBucket, config.SelectionPolicy)

	// cold
	if len(m.coldBucket) > 0 {
		m.coldSelector = selector.New(m.coldBucket, config.SelectionPolicy)
	} else {
		m.log.Infof("no cold bucket configured")
	}

	// has enabled migration
	if config.Migration != nil && config.Migration.Enabled {
		for _, bucket := range m.Buckets() {
			_ = bucket.SetMigration(m)
		}
	}
	return nil
}

// Select implements [storage.Migrator].
func (m *migratorStorage) Select(ctx context.Context, id *object.ID) storage.Bucket {
	// find bucket: Hot → Warm → Cold
	return m.chainSelector(ctx, id,
		m.hotSelector,
		m.warmSelector,
		m.coldSelector,
	)
}

// Demote implements [storage.Migrator].
func (m *migratorStorage) Demote(ctx context.Context, id *object.ID, src storage.Bucket) error {
	// Hot -> Warm -> Cold
	var layer string
	switch src.StoreType() {
	case storage.TypeHot:
		layer = storage.TypeWarm
	case storage.TypeWarm:
		layer = storage.TypeCold
	default:
		return nil // no demotion for other types
	}

	target := m.SelectLayer(ctx, id, layer)
	if target == nil {
		return fmt.Errorf("no target bucket found for demotion from %s to %s", src.StoreType(), layer)
	}

	return src.Migrate(ctx, id, target)
}

// Promote implements [storage.Migrator].
func (m *migratorStorage) Promote(ctx context.Context, id *object.ID, src storage.Bucket) error {
	// Cold -> Warm -> Hot
	var layer string
	switch src.StoreType() {
	case storage.TypeCold:
		layer = storage.TypeWarm
	case storage.TypeWarm:
		layer = storage.TypeHot
	default:
		return nil // no promotion for other types
	}

	target := m.SelectLayer(ctx, id, layer)
	if target == nil {
		return fmt.Errorf("no target bucket found for promotion from %s to %s", src.StoreType(), layer)
	}

	return src.Migrate(ctx, id, target)
}

func (m *migratorStorage) SelectLayer(ctx context.Context, id *object.ID, layer string) storage.Bucket {
	switch layer {
	case storage.TypeHot:
		if m.hotSelector != nil {
			return m.hotSelector.Select(ctx, id)
		}
	case storage.TypeNormal, storage.TypeWarm: // TypeWarm is same as TypeNormal
		if m.warmSelector != nil {
			return m.warmSelector.Select(ctx, id)
		}
	case storage.TypeCold:
		if m.coldSelector != nil {
			return m.coldSelector.Select(ctx, id)
		}
	case storage.TypeInMemory:
		return m.memoryBucket
	}
	return nil
}

func (m *migratorStorage) chainSelector(ctx context.Context, id *object.ID, selectors ...storage.Selector) storage.Bucket {
	for _, sel := range selectors {
		if sel == nil {
			continue
		}
		if bucket := sel.Select(ctx, id); bucket != nil && bucket.Exist(ctx, id.Bytes()) {
			return bucket
		}
	}

	// fallback to warm selector
	return m.warmSelector.Select(ctx, id)
}

// Buckets implements [storage.Migrator].
func (m *migratorStorage) Buckets() []storage.Bucket {
	buckets := make([]storage.Bucket, 0, len(m.warmBucket)+len(m.hotBucket)+len(m.coldBucket))
	buckets = append(buckets, m.warmBucket...)
	buckets = append(buckets, m.hotBucket...)
	buckets = append(buckets, m.coldBucket...)
	return buckets
}

// PURGE implements [storage.Migrator].
func (m *migratorStorage) PURGE(storeUrl string, typ storage.PurgeControl) error {
	// Directory prefix purge
	if typ.Dir {
		// For mark-expired on dir, skip sharedkv hits and fallback to full scan below.
		if typ.MarkExpired {
			return nil
		}

		// For directory purge, we prefer SharedKV inverted index when available:
		// key schema: ix/<bucketID>/<storeUrl>
		// value: object.IDHash bytes
		ctx := context.Background()
		processed := 0

		for _, b := range m.Buckets() {
			prefix := fmt.Sprintf("ix/%s/%s", b.ID(), storeUrl)
			_ = m.sharedkv.IteratePrefix(ctx, []byte(prefix), func(key, val []byte) error {
				// parse hash
				var h object.IDHash
				if len(val) >= object.IdHashSize {
					copy(h[:], val[:object.IdHashSize])
				} else {
					// skip invalid record
					return nil
				}

				if typ.Hard || !typ.MarkExpired {
					if err := b.DiscardWithHash(ctx, h); err == nil {
						processed++
					}
				}

				// remove index mapping
				_ = m.sharedkv.Delete(ctx, key)
				return nil
			})
		}

		// fallback: scan indexdb if no sharedkv hits, or to ensure completeness
		if processed == 0 {
			for _, b := range m.Buckets() {
				_ = b.Iterate(ctx, func(md *object.Metadata) error {
					if md == nil {
						return nil
					}
					if strings.HasPrefix(md.ID.Path(), storeUrl) {
						if typ.Hard || !typ.MarkExpired {
							_ = b.DiscardWithMetadata(ctx, md)
						} else {
							md.ExpiresAt = time.Now().Add(-1).Unix()
							_ = b.Store(ctx, md)
						}
						processed++
					}
					return nil
				})
			}
		}

		if processed == 0 {
			return storage.ErrKeyNotFound
		}
		return nil
	}

	// Single object purge
	cacheKey := object.NewID(storeUrl)

	bucket := m.Select(context.Background(), cacheKey)
	if bucket == nil {
		return fmt.Errorf("bucket not found")
	}

	// hard delete cache file mode.
	if typ.Hard {
		return bucket.Discard(context.Background(), cacheKey)
	}

	// MarkExpired to revalidate.
	// soft delete cache file mode.
	md, err := bucket.Lookup(context.Background(), cacheKey)
	if err != nil {
		return err
	}

	// set expire time to past time. and then store it back.
	md.ExpiresAt = time.Now().Add(-1).Unix()
	// TODO: we should acquire a globalResourceLock before updating.
	return bucket.Store(context.Background(), md)
}

// Rebuild implements [storage.Migrator].
func (m *migratorStorage) Rebuild(ctx context.Context, buckets []storage.Bucket) error {
	return nil
}

// SharedKV implements [storage.Migrator].
func (m *migratorStorage) SharedKV() storage.SharedKV {
	return m.sharedkv
}

// Close implements [storage.Migrator].
func (m *migratorStorage) Close() error {
	var errs []error
	// close all buckets
	for _, bucket := range m.warmBucket {
		errs = append(errs, bucket.Close())
	}

	for _, bucket := range m.hotBucket {
		errs = append(errs, bucket.Close())
	}

	for _, bucket := range m.coldBucket {
		errs = append(errs, bucket.Close())
	}

	if m.memoryBucket != nil {
		if err := m.memoryBucket.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	// memdb close
	if err := m.sharedkv.Close(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
