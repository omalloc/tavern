package storage

import (
	"context"
	"errors"
	"fmt"
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

var _ storage.Storage = (*nativeStorage)(nil)

type nativeStorage struct {
	closed bool
	mu     sync.Mutex
	log    *log.Helper

	selector     storage.Selector
	sharedkv     storage.SharedKV
	nopBucket    storage.Bucket
	memoryBucket []storage.Bucket
	hotBucket    []storage.Bucket
	normalBucket []storage.Bucket
}

func New(config *conf.Storage, logger log.Logger) (storage.Storage, error) {
	nopBucket, _ := empty.New(&conf.Bucket{}, sharedkv.NewEmpty())
	n := &nativeStorage{
		closed: false,
		mu:     sync.Mutex{},
		log:    log.NewHelper(logger),

		selector:     selector.New([]storage.Bucket{}, config.SelectionPolicy),
		sharedkv:     sharedkv.NewMemSharedKV(),
		nopBucket:    nopBucket,
		memoryBucket: make([]storage.Bucket, 0, len(config.Buckets)),
		hotBucket:    make([]storage.Bucket, 0, len(config.Buckets)),
		normalBucket: make([]storage.Bucket, 0, len(config.Buckets)),
	}

	if err := n.reinit(config); err != nil {
		return nil, err
	}

	return n, nil
}

func (n *nativeStorage) reinit(config *conf.Storage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := n.sharedkv.DropPrefix(ctx, []byte("if/domain/")); err != nil {
		n.log.Warnf("failed to drop prefix key `if/domain/` counter: %s", err)
	}

	globalConfig := &globalBucketOption{
		AsyncLoad:       config.AsyncLoad,
		EvictionPolicy:  config.EvictionPolicy,
		SelectionPolicy: config.SelectionPolicy,
		Driver:          config.Driver,
		DBType:          config.DBType,
	}

	for _, c := range config.Buckets {
		bucket, err := NewBucket(mergeConfig(globalConfig, c), n.sharedkv)
		if err != nil {
			return err
		}

		switch bucket.StoreType() {
		case "normal":
			n.normalBucket = append(n.normalBucket, bucket)
		case "hot":
			n.hotBucket = append(n.hotBucket, bucket)
		case "fastmemory":
			n.memoryBucket = append(n.memoryBucket, bucket)
		}
	}

	// wait for all buckets to be initialized
	// load indexdb
	// load lru
	// load purge queue

	n.selector = selector.New(n.normalBucket, config.SelectionPolicy)

	return nil
}

// Select implements storage.Selector.
func (n *nativeStorage) Select(ctx context.Context, id *object.ID) storage.Bucket {
	bucket := n.selector.Select(ctx, id)
	return bucket
}

// Rebuild implements storage.Selector.
func (n *nativeStorage) Rebuild(ctx context.Context, buckets []storage.Bucket) error {
	return nil
}

// Buckets implements storage.Storage.
func (n *nativeStorage) Buckets() []storage.Bucket {
	return append(n.normalBucket, n.hotBucket...)
}

// PURGE implements storage.Storage.
func (n *nativeStorage) PURGE(storeUrl string, typ storage.PurgeControl) error {
	// Directory prefix purge
	if typ.Dir {
		// For directory purge, we prefer SharedKV inverted index when available:
		// key schema: ix/<bucketID>/<storeUrl>
		// value: object.IDHash bytes
		ctx := context.Background()
		processed := 0

		for _, b := range n.Buckets() {
			prefix := fmt.Sprintf("ix/%s/%s", b.ID(), storeUrl)
			_ = n.sharedkv.IteratePrefix(ctx, []byte(prefix), func(key, val []byte) error {
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
				} else {
					// For mark-expired on dir, skip sharedkv hits and fallback to full scan below.
				}

				// remove index mapping
				_ = n.sharedkv.Delete(ctx, key)
				return nil
			})
		}

		// fallback: scan indexdb if no sharedkv hits, or to ensure completeness
		if processed == 0 {
			for _, b := range n.Buckets() {
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

	bucket := n.Select(context.Background(), cacheKey)
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

func (n *nativeStorage) SharedKV() storage.SharedKV {
	return n.sharedkv
}

// Close implements storage.Storage.
func (n *nativeStorage) Close() error {
	var errs []error
	// close all buckets
	for _, bucket := range n.normalBucket {
		errs = append(errs, bucket.Close())
	}

	for _, bucket := range n.hotBucket {
		errs = append(errs, bucket.Close())
	}

	for _, bucket := range n.memoryBucket {
		errs = append(errs, bucket.Close())
	}

	// memdb close
	if err := n.sharedkv.Close(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
