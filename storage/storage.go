package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/lifecycle"
	"github.com/omalloc/tavern/storage/bucket/empty"
	"github.com/omalloc/tavern/storage/diraware"
	"github.com/omalloc/tavern/storage/representation"
	"github.com/omalloc/tavern/storage/selector"
	"github.com/omalloc/tavern/storage/sharedkv"
)

var _ storage.Storage = (*nativeStorage)(nil)

type nativeStorage struct {
	representations representationRegistry
	tasks           lifecycle.Group
	manager         *representation.Manager
	closed          bool
	closeOnce       sync.Once
	closeErr        error
	mu              sync.Mutex
	log             *log.Helper

	selector     storage.Selector
	sharedkv     storage.SharedKV
	nopBucket    storage.Bucket
	memoryBucket storage.Bucket
	hotBucket    []storage.Bucket
	warmlBucket  []storage.Bucket
}

func New(config *conf.Storage, logger log.Logger) (storage.Storage, error) {
	// 填充默认配置
	config.FillDefault()

	// support migrator mode
	if config.Migration != nil && config.Migration.Enabled {
		return NewMigrator(config, logger)
	}

	nopBucket, _ := empty.New(&storage.BucketConfig{}, sharedkv.NewEmpty())
	n := &nativeStorage{
		closed: false,
		mu:     sync.Mutex{},
		log:    log.NewHelper(logger),

		selector:     selector.New([]storage.Bucket{}, config.SelectionPolicy),
		sharedkv:     sharedkv.NewMemSharedKV(),
		nopBucket:    nopBucket,
		memoryBucket: nil,
		hotBucket:    make([]storage.Bucket, 0, len(config.Buckets)),
		warmlBucket:  make([]storage.Bucket, 0, len(config.Buckets)),
	}

	if err := n.reinit(config); err != nil {
		n.Close()
		return nil, err
	}

	var err error
	n.manager, err = initializeRepresentations(config.Representation, n.RepresentationBuckets(), &n.representations)
	if err != nil {
		n.Close()
		return nil, err
	}

	// diraware adapter
	if config.DirAware != nil && config.DirAware.Enabled {
		if config.DirAware.StorePath != "" {
			_ = os.MkdirAll(config.DirAware.StorePath, 0755)
			// replace memkv with storekv
			n.sharedkv = sharedkv.NewStoreSharedKV(config.DirAware.StorePath)
		}

		// sharedkv used no-mem typ.
		return diraware.New(n, diraware.NewChecker(n.sharedkv,
			diraware.WithAutoClear(config.DirAware.AutoClear),
		)), nil
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
	}

	for _, c := range config.Buckets {
		bucket, err := NewBucket(mergeConfig(globalConfig, c), n.sharedkv)
		if err != nil {
			return err
		}

		switch bucket.StoreType() {
		case storage.TypeNormal, storage.TypeWarm:
			n.warmlBucket = append(n.warmlBucket, bucket)
		case storage.TypeHot:
			n.hotBucket = append(n.hotBucket, bucket)
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

	if len(n.warmlBucket) <= 0 {
		// no normal bucket, use nop bucket
		if n.memoryBucket != nil {
			n.warmlBucket = append(n.warmlBucket, n.memoryBucket)
		}
	}

	n.selector = selector.New(n.warmlBucket, config.SelectionPolicy)

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
	buckets := make([]storage.Bucket, 0, len(n.warmlBucket)+len(n.hotBucket))
	buckets = append(buckets, n.warmlBucket...)
	return append(buckets, n.hotBucket...)
}

func (n *nativeStorage) RepresentationBuckets() []storage.Bucket { return n.Buckets() }

// PURGE implements storage.Storage.
func (n *nativeStorage) PURGE(storeUrl string, typ storage.PurgeControl) error {
	return n.representations.purge(context.Background(), n.Buckets(), storeUrl, typ)
}

func (n *nativeStorage) SharedKV() storage.SharedKV {
	return n.sharedkv
}

// Close implements storage.Storage.
func (n *nativeStorage) Close() error {
	n.closeOnce.Do(func() { n.closeErr = n.closeStorage() })
	return n.closeErr
}

func (n *nativeStorage) closeStorage() error {
	if err := n.Drain(context.Background()); err != nil {
		return err
	}
	if n.manager != nil {
		n.manager.Close()
	}
	var errs []error
	// close all buckets
	for _, bucket := range n.warmlBucket {
		errs = append(errs, bucket.Close())
	}

	for _, bucket := range n.hotBucket {
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
