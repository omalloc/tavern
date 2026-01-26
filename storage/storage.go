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

	warmSelector storage.Selector // warm selector
	hotSelector  storage.Selector // hot selector
	coldSelector storage.Selector // cold selector
	sharedkv     storage.SharedKV
	nopBucket    storage.Bucket
	memoryBucket storage.Bucket
	hotBucket    []storage.Bucket
	normalBucket []storage.Bucket
	coldBucket   []storage.Bucket
}

func New(config *conf.Storage, logger log.Logger) (storage.Storage, error) {
	nopBucket, _ := empty.New(&conf.Bucket{}, sharedkv.NewEmpty())
	n := &nativeStorage{
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
		DBPath:          config.DBPath,
		Tiering:         config.Tiering,
	}

	for _, c := range config.Buckets {
		bucket, err := NewBucket(mergeConfig(globalConfig, c), n.sharedkv)
		if err != nil {
			return err
		}

		switch bucket.StoreType() {
		case storage.TypeNormal, storage.TypeWarm:
			n.normalBucket = append(n.normalBucket, bucket)
		case storage.TypeHot:
			n.hotBucket = append(n.hotBucket, bucket)
		case storage.TypeCold:
			n.coldBucket = append(n.coldBucket, bucket)
		case storage.TypeInMemory:
			if n.memoryBucket != nil {
				return fmt.Errorf("only one inmemory bucket is allowed")
			}
			n.memoryBucket = bucket
		}
	}

	// wait for all buckets to be initialized
	// load indexdb
	// load lru
	// load purge queue

	// warm / normal
	if len(n.normalBucket) <= 0 {
		n.log.Infof("no warm bucket configured")
		// no normal bucket, use nop bucket
		if n.memoryBucket != nil {
			n.normalBucket = append(n.normalBucket, n.memoryBucket)
		}
	}
	n.warmSelector = selector.New(n.normalBucket, config.SelectionPolicy)

	// cold
	if len(n.coldBucket) > 0 {
		n.coldSelector = selector.New(n.coldBucket, config.SelectionPolicy)
	} else {
		n.log.Infof("no cold bucket configured")
	}

	// hot
	if len(n.hotBucket) > 0 {
		n.hotSelector = selector.New(n.hotBucket, config.SelectionPolicy)
	} else {
		n.log.Infof("no hot bucket configured")
	}

	// register demoter
	for _, b := range n.Buckets() {
		b.SetDemoter(n)
		// register promoter as well
		b.SetPromoter(n)
	}
	if n.memoryBucket != nil {
		n.memoryBucket.SetDemoter(n)
		n.memoryBucket.SetPromoter(n)
	}

	return nil
}

// Select implements storage.Selector.
func (n *nativeStorage) Select(ctx context.Context, id *object.ID) storage.Bucket {
	// find bucket: Hot → Warm → Cold
	return n.chainSelector(ctx, id,
		n.hotSelector,
		n.warmSelector,
		n.coldSelector,
	)
}

// SelectByTier implements storage.Storage.
func (n *nativeStorage) SelectWithType(ctx context.Context, id *object.ID, tier string) storage.Bucket {
	switch tier {
	case storage.TypeHot:
		if n.hotSelector != nil {
			return n.hotSelector.Select(ctx, id)
		}
	case storage.TypeNormal, storage.TypeWarm: // TypeWarm is same as TypeNormal
		if n.warmSelector != nil {
			return n.warmSelector.Select(ctx, id)
		}
	case storage.TypeCold:
		if n.coldSelector != nil {
			return n.coldSelector.Select(ctx, id)
		}
	case storage.TypeInMemory:
		return n.memoryBucket
	}
	return nil
}

// Demote implements storage.Demoter.
func (n *nativeStorage) Demote(ctx context.Context, id *object.ID, src storage.Bucket) error {
	// Hot -> Warm -> Cold
	var targetTier string
	switch src.StoreType() {
	case storage.TypeHot:
		targetTier = storage.TypeNormal
	case storage.TypeNormal: // TypeWarm is same as TypeNormal
		targetTier = storage.TypeCold
	default:
		return nil // no demotion for other types
	}

	target := n.SelectWithType(ctx, id, targetTier)
	if target == nil {
		return fmt.Errorf("no target bucket found for demotion from %s to %s", src.StoreType(), targetTier)
	}

	return src.MoveTo(ctx, id, target)
}

// Promote implements storage.Promoter.
func (n *nativeStorage) Promote(ctx context.Context, id *object.ID, src storage.Bucket) error {
	// Cold -> Warm -> Hot
	var targetTier string
	switch src.StoreType() {
	case storage.TypeCold:
		targetTier = storage.TypeNormal
	case storage.TypeNormal: // TypeWarm is same as TypeNormal
		targetTier = storage.TypeHot
	default:
		return nil // no promotion for other types
	}

	target := n.SelectWithType(ctx, id, targetTier)
	if target == nil {
		return fmt.Errorf("no target bucket found for promotion from %s to %s", src.StoreType(), targetTier)
	}

	return src.MoveTo(ctx, id, target)
}

func (n *nativeStorage) chainSelector(ctx context.Context, id *object.ID, selectors ...storage.Selector) storage.Bucket {
	for _, sel := range selectors {
		if sel == nil {
			continue
		}
		if bucket := sel.Select(ctx, id); bucket != nil && bucket.Exist(ctx, id.Bytes()) {
			return bucket
		}
	}

	// fallback to warm selector
	return n.warmSelector.Select(ctx, id)
}

// Rebuild implements storage.Selector.
func (n *nativeStorage) Rebuild(ctx context.Context, buckets []storage.Bucket) error {
	return nil
}

// Buckets implements storage.Storage.
func (n *nativeStorage) Buckets() []storage.Bucket {
	buckets := make([]storage.Bucket, 0, len(n.normalBucket)+len(n.hotBucket)+len(n.coldBucket))
	buckets = append(buckets, n.normalBucket...)
	buckets = append(buckets, n.hotBucket...)
	buckets = append(buckets, n.coldBucket...)
	return buckets
}

// PURGE implements storage.Storage.
func (n *nativeStorage) PURGE(storeUrl string, typ storage.PurgeControl) error {
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

	for _, bucket := range n.coldBucket {
		errs = append(errs, bucket.Close())
	}

	if n.memoryBucket != nil {
		if err := n.memoryBucket.Close(); err != nil {
			errs = append(errs, err)
		}
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
