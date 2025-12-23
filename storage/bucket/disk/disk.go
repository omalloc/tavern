package disk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/paulbellamy/ratecounter"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/algorithm/lru"
	"github.com/omalloc/tavern/storage/indexdb"
)

var _ storage.Bucket = (*diskBucket)(nil)

type diskBucket struct {
	path      string
	dbPath    string
	driver    string
	storeType string
	asyncLoad bool
	weight    int
	sharedkv  storage.SharedKV
	indexdb   storage.IndexDB
	cache     *lru.Cache[object.IDHash, storage.Mark]
	fileMode  fs.FileMode
	stop      chan struct{}
}

func New(config *conf.Bucket, sharedkv storage.SharedKV) (storage.Bucket, error) {
	dbPath := path.Join(config.Path, ".indexdb/")

	bucket := &diskBucket{
		path:      config.Path,
		dbPath:    dbPath,
		driver:    config.Driver,
		storeType: config.Type,
		asyncLoad: config.AsyncLoad,
		weight:    100, // default weight
		sharedkv:  sharedkv,
		cache:     lru.New[object.IDHash, storage.Mark](100_000),
		fileMode:  fs.FileMode(0o755),
		stop:      make(chan struct{}, 1),
	}

	bucket.initWorkdir()

	// create indexdb
	db, err := indexdb.Create(config.DBType, indexdb.NewOption(dbPath, indexdb.WithType("pebble")))
	if err != nil {
		log.Errorf("failed to create %s indexdb %v", config.DBType, err)
		return nil, err
	}
	bucket.indexdb = db

	// evict
	go bucket.evict()

	// load lru
	bucket.loadLRU()

	return bucket, nil
}

func (d *diskBucket) evict() {
	clog := log.Context(context.Background())

	ch := make(chan lru.Eviction[object.IDHash, storage.Mark], 100)
	d.cache.EvictionChannel = ch

	clog.Debugf("start evict goroutine for %s", d.ID())

	go func() {
		for {
			select {
			case <-d.stop:
				return
			case evicted := <-ch:
				fd := evicted.Key.WPath(d.path)
				clog.Debugf("evict file %s, last-access %d", fd, evicted.Value.LastAccess())
				// TODO: discard expired cachefile or Move to cold storage
				d.DiscardWithHash(context.Background(), evicted.Key)
			}
		}
	}()
}

func (d *diskBucket) loadLRU() {
	load := func(async bool) {
		mdCount, blockCount := 0, 0
		counter := ratecounter.NewRateCounter(1 * time.Second)
		blockCounter := ratecounter.NewRateCounter(1 * time.Second)
		stop := make(chan struct{}, 1)
		runMode := formatSync(async)

		log.Infof("start %s load metadata from %s", runMode, d.ID())
		go func() {
			tick := time.NewTicker(time.Second)
			for {
				select {
				case <-stop:
					tick.Stop()
					log.Infof("bucket %s %s load metadata(%d/block-%d) done. per-second %d(%d)/s", d.ID(), runMode, mdCount, blockCount, counter.Rate(), blockCounter.Rate())
					return
				case <-tick.C:
					log.Infof("bucket %s %s load metadata(%d/block-%d). per-second %d(%d)/s", d.ID(), runMode, mdCount, blockCount, counter.Rate(), blockCounter.Rate())
				}
			}
		}()

		// iterate all keys
		_ = d.indexdb.Iterate(context.Background(), nil, func(key []byte, meta *object.Metadata) bool {
			if meta != nil {
				mdCount++
				blockCount += int(meta.Parts.Count())
				d.cache.Set(meta.ID.Hash(), storage.NewMark(meta.LastRefUnix, uint64(meta.Refs)))
				u, _ := url.Parse(meta.ID.Path())
				_, _ = d.sharedkv.Incr(context.Background(), []byte(fmt.Sprintf("if/domain/%s", u.Host)), 1)
				counter.Incr(1)
				blockCounter.Incr(int64(meta.Parts.Count()))
			}
			return true
		})

		stop <- struct{}{}
	}

	if d.asyncLoad {
		go load(true)
	} else {
		load(false)
	}
}

// Discard implements storage.Bucket.
func (d *diskBucket) Discard(ctx context.Context, id *object.ID) error {
	md, err := d.indexdb.Get(ctx, id.Bytes())
	if err != nil {
		return err
	}

	return d.discard(ctx, md)
}

// DiscardWithHash implements storage.Bucket.
func (d *diskBucket) DiscardWithHash(ctx context.Context, hash object.IDHash) error {
	id := hash[:]
	wpath := hash.WPath(d.path)

	if log.Enabled(log.LevelDebug) {
		log.Debugf("discard %s", wpath)
	}

	md, err := d.indexdb.Get(ctx, id)
	if err != nil {
		return err
	}

	// 缓存不存在
	if md == nil {
		return os.ErrNotExist
	}

	// part 是否存在
	if md.Parts.Count() <= 0 {
		return d.indexdb.Delete(ctx, id)
	}

	// how get caching locker

	// 存在
	if err = os.Remove(wpath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Context(ctx).Errorf("failed to remove file %s: %v", wpath, err)
	}

	// 删除 part
	md.Parts.Clear()
	return d.indexdb.Delete(ctx, id)
}

// DiscardWithMessage implements storage.Bucket.
func (d *diskBucket) DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error {
	log.Context(ctx).Infof("discard %s [path=%s] with message %s", id, id.WPath(d.path), msg)
	return d.Discard(ctx, id)
}

// DiscardWithMetadata implements storage.Bucket.
func (d *diskBucket) DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error {
	return d.Discard(ctx, meta.ID)
}

func (d *diskBucket) discard(ctx context.Context, md *object.Metadata) error {
	// 缓存不存在
	if md == nil {
		return os.ErrNotExist
	}

	clog := log.Context(ctx)

	// 先删除 db 中的数据, 避免被其他协程 HIT
	if err := d.indexdb.Delete(ctx, md.ID.Bytes()); err != nil {
		clog.Warnf("failed to delete metadata %s: %v", md.ID.WPath(d.path), err)
	}

	// 如果缓存为1级，则清除全部子缓存(vary)
	if md.IsVary() && len(md.VirtualKey) > 0 {
		for _, varyKey := range md.VirtualKey {
			oid := object.NewVirtualID(md.ID.Path(), varyKey)
			if strings.EqualFold(oid.HashStr(), md.ID.HashStr()) {
				clog.Warnf("discard %s but level1 id equal level2 id", md.ID.WPath(d.path))
				continue
			}
			// discard leveled cache (vary,chunked)
			_ = d.Discard(ctx, oid)
		}
	}

	fpath := md.ID.WPath(d.path)
	if err := os.Remove(fpath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Context(ctx).Errorf("failed to remove cached file %s: %v", fpath, err)
	}

	u, _ := url.Parse(md.ID.Path())
	_, _ = d.sharedkv.Decr(ctx, []byte(fmt.Sprintf("if/domain/%s", u.Host)), 1)

	return nil
}

// Exist implements storage.Bucket.
func (d *diskBucket) Exist(ctx context.Context, id []byte) bool {
	return d.indexdb.Exist(ctx, id)
}

// Expired implements storage.Bucket.
func (d *diskBucket) Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool {
	// TODO: check has expired
	return false
}

// Iterate implements storage.Bucket.
func (d *diskBucket) Iterate(ctx context.Context, fn func(*object.Metadata) error) error {
	return d.indexdb.Iterate(ctx, nil, func(key []byte, val *object.Metadata) bool {
		return fn(val) == nil
	})
}

// Lookup implements storage.Bucket.
func (d *diskBucket) Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	md, err := d.indexdb.Get(ctx, id.Bytes())
	return md, err
}

// Remove implements storage.Bucket.
func (d *diskBucket) Remove(ctx context.Context, id *object.ID) error {
	return d.indexdb.Delete(ctx, id.Bytes())
}

// Store implements storage.Bucket.
func (d *diskBucket) Store(ctx context.Context, meta *object.Metadata) error {
	if log.Enabled(log.LevelDebug) {
		clog := log.Context(ctx)

		now := time.Now()
		defer func() {
			cost := time.Since(now)

			clog.Debugf("store metadata %s, cost %s", meta.ID.WPath(d.path), cost)
		}()
	}

	meta.Headers.Del("X-Protocol")
	meta.Headers.Del("X-Protocol-Cache")
	meta.Headers.Del("X-Protocol-Request-Id")

	if !d.cache.Has(meta.ID.Hash()) {
		d.cache.Set(meta.ID.Hash(), storage.NewMark(meta.LastRefUnix, uint64(meta.Refs)))
	}

	return d.indexdb.Set(ctx, meta.ID.Bytes(), meta)
}

// HasBad implements storage.Bucket.
func (d *diskBucket) HasBad() bool {
	return false
}

// ID implements storage.Bucket.
func (d *diskBucket) ID() string {
	return d.path
}

// StoreType implements storage.Bucket.
func (d *diskBucket) StoreType() string {
	return d.storeType
}

// Type implements storage.Bucket.
func (d *diskBucket) Type() string {
	return d.driver
}

// UseAllow implements storage.Bucket.
func (d *diskBucket) UseAllow() bool {
	// TODO: check disk usage if the bucket is full, return false
	return true
}

// Weight implements storage.Bucket.
func (d *diskBucket) Weight() int {
	return d.weight
}

// Allow implements storage.Bucket.
func (d *diskBucket) Allow() int {
	panic("unimplemented")
}

func (d *diskBucket) Path() string {
	return d.path
}

// Close implements storage.Bucket.
func (d *diskBucket) Close() error {
	return d.indexdb.Close()
}

func (d *diskBucket) initWorkdir() {
	defer func() {
		if rec := recover(); rec != nil {
			log.Errorf("failed to create directory %s: %v", d.path, rec)
		}
	}()

	if err := os.MkdirAll(d.path, d.fileMode); err != nil && !errors.Is(err, os.ErrExist) {
		log.Errorf("failed to create directory %s: %v", d.path, err)
	}
	if err := os.MkdirAll(d.dbPath, d.fileMode); err != nil && !errors.Is(err, os.ErrExist) {
		log.Errorf("failed to create directory %s: %v", d.path, err)
	}
}

func formatSync(async bool) string {
	if async {
		return "async"
	}
	return "sync"
}

func IDPath(path string, id *object.ID) string {
	hash := id.HashStr()
	return filepath.Join(path, hash[0:1], hash[2:4], hash)
}

func IDPathRandomSuffix(path string) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return path + "_" + hex.EncodeToString(buf)
}
