package pebble

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/storage/indexdb"
)

var _ storage.IndexDB = (*PebbleDB)(nil)
var _ storage.SyncIndexDB = (*PebbleDB)(nil)
var _ storage.IndexScanner = (*PebbleDB)(nil)

type PebbleDB struct {
	codec         encoding.Codec
	db            *pebble.DB
	writeMode     *pebble.WriteOptions
	skipErrRecord bool
	closed        sync.Once
	persistent    bool
	writeback     writebackQueue
}

func init() {
	indexdb.Register("pebble", New)
}

// Get implements storage.IndexDB.
func (p *PebbleDB) Get(ctx context.Context, key []byte) (*object.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.writeback.mu.Lock()
	if value, exists := p.writeback.pending[string(key)]; exists {
		buf := bytes.Clone(value)
		p.writeback.mu.Unlock()
		if value == nil {
			return nil, storage.ErrKeyNotFound
		}
		md := &object.Metadata{}
		err := p.codec.Unmarshal(buf, md)
		return md, err
	}
	p.writeback.mu.Unlock()
	buf, closer, err := p.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, storage.ErrKeyNotFound
		}
		return nil, err
	}
	defer closer.Close()

	meta := &object.Metadata{}
	err = p.codec.Unmarshal(buf, meta)
	return meta, err
}

// Set implements storage.IndexDB.
func (p *PebbleDB) Set(ctx context.Context, key []byte, val *object.Metadata) error {
	buf, err := p.codec.Marshal(val)
	if err != nil {
		return err
	}

	// Cache records use writeback even if legacy ordinary writes request Sync.
	syncWrite := p.writeMode.GetSync() && val.RecordKind != object.RecordRepresentation && val.RecordKind != object.RecordResource
	return p.write(ctx, key, buf, syncWrite)
}

func (p *PebbleDB) Persistent() bool { return p.persistent }

func (p *PebbleDB) SetSync(ctx context.Context, key []byte, val *object.Metadata) error {
	if !p.persistent {
		return storage.ErrDurabilityUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	buf, err := p.codec.Marshal(val)
	if err != nil {
		return err
	}
	return p.write(ctx, key, buf, true)
}

func (p *PebbleDB) Scan(ctx context.Context, after []byte, limit int) ([]*object.Metadata, []byte, error) {
	if limit <= 0 {
		return nil, nil, errors.New("metadata scan limit must be positive")
	}
	iter, closeIter, err := p.readIterator()
	if err != nil {
		return nil, nil, err
	}
	defer closeIter()
	valid := iter.First()
	if len(after) > 0 {
		valid = iter.SeekGE(after)
		if valid && bytes.Equal(iter.Key(), after) {
			valid = iter.Next()
		}
	}
	var records []*object.Metadata
	var cursor []byte
	for valid && len(records) < limit {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		data, err := iter.ValueAndErr()
		if err != nil {
			return nil, nil, err
		}
		md := &object.Metadata{}
		if err := p.codec.Unmarshal(data, md); err != nil {
			return nil, nil, err
		}
		records = append(records, md)
		cursor = append(cursor[:0], iter.Key()...)
		valid = iter.Next()
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	if !valid {
		cursor = nil
	}
	return records, cursor, nil
}

// Iterate implements storage.IndexDB.
func (p *PebbleDB) Iterate(ctx context.Context, prefix []byte, f storage.IterateFunc) error {
	iter, closeIter, err := p.readIterator()
	if err != nil {
		return err
	}
	defer closeIter()

	if p.skipErrRecord {
		for iter.First(); iter.Valid(); iter.Next() {
			buf, err1 := iter.ValueAndErr()
			if err1 != nil {
				continue
			}

			meta := &object.Metadata{}
			if err = p.codec.Unmarshal(buf, meta); err != nil {
				continue
			}
			f(iter.Key(), meta)
		}
		return nil
	}

	for iter.First(); iter.Valid(); iter.Next() {
		buf, err1 := iter.ValueAndErr()
		if err1 != nil {
			return err
		}

		meta := &object.Metadata{}
		if err = p.codec.Unmarshal(buf, meta); err != nil {
			return err
		}

		f(iter.Key(), meta)
	}
	return nil
}

// Delete implements storage.IndexDB.
func (p *PebbleDB) Delete(ctx context.Context, key []byte) error {
	return p.write(ctx, key, nil, p.writeMode.GetSync())
}

// Exist implements storage.IndexDB.
func (p *PebbleDB) Exist(ctx context.Context, key []byte) bool {
	_, err := p.Get(ctx, key)
	return err == nil
}

// Expired implements storage.IndexDB.
func (p *PebbleDB) Expired(ctx context.Context, f storage.IterateFunc) error {
	panic("unimplemented")
}

// Close implements storage.IndexDB.
func (p *PebbleDB) Close() error {
	var err error
	p.closed.Do(func() {
		if p.writeback.stop != nil {
			close(p.writeback.stop)
			<-p.writeback.done
		}
		err = p.flushWriteback()
		err = errors.Join(err, p.db.Flush())
		// force flush data to disk
		err = errors.Join(err, p.db.Close())
	})
	return err
}

// GC implements storage.IndexDB.
func (p *PebbleDB) GC(ctx context.Context) error {
	return nil
}

// pebbleOption  Options for pebble
type pebbleOption struct {
	CacheSize          int  `json:"cache_size" yaml:"cache_size"`
	MemTableSize       int  `json:"mem_table_size" yaml:"mem_table_size"`
	BytesPerSync       int  `json:"bytes_per_sync" yaml:"bytes_per_sync"`
	WalBytesPerSync    int  `json:"wal_bytes_per_sync" yaml:"wal_bytes_per_sync"`
	WalMinSyncInterval int  `json:"wal_min_sync_interval" yaml:"wal_min_sync_interval"`
	WriteSyncMode      bool `json:"write_sync_mode" yaml:"write_sync_mode"`
}

func New(path string, option storage.Option) (storage.IndexDB, error) {
	var pebbleOption pebbleOption
	if err := option.Unmarshal(&pebbleOption); err != nil {
		log.Warnf("pebble option unmarshal failed, use default config: %v", err)
		// defualt config.
		defOpt := pebble.DefaultOptions()
		pebbleOption.WriteSyncMode = true
		pebbleOption.CacheSize = int(defOpt.CacheSize)
		pebbleOption.MemTableSize = int(defOpt.MemTableSize)
		pebbleOption.BytesPerSync = int(defOpt.BytesPerSync)
		pebbleOption.WalBytesPerSync = int(defOpt.WALBytesPerSync)
		pebbleOption.WalMinSyncInterval = 0
	}

	var FS = vfs.Default
	if path == storage.TypeInMemory {
		path = ""
		FS = vfs.NewMem()
	}

	pdb, err := pebble.Open(path, &pebble.Options{
		Logger:          log.NewHelper(log.NewFilter(log.GetLogger(), log.FilterLevel(log.LevelWarn))),
		FS:              FS,
		CacheSize:       int64(pebbleOption.CacheSize),
		MemTableSize:    uint64(pebbleOption.MemTableSize),
		BytesPerSync:    pebbleOption.BytesPerSync,
		WALBytesPerSync: pebbleOption.WalBytesPerSync,
		WALMinSyncInterval: func() time.Duration {
			return time.Duration(pebbleOption.WalMinSyncInterval) * time.Second
		},
	})
	if err != nil {
		return nil, err
	}

	// write options
	writeMode := pebble.Sync
	if !pebbleOption.WriteSyncMode {
		writeMode = pebble.NoSync
	}

	p := &PebbleDB{
		codec:         option.Codec(),
		db:            pdb,
		writeMode:     writeMode, // 是否异步写操作
		skipErrRecord: true,
		closed:        sync.Once{},
		persistent:    option.DBPath() != storage.TypeInMemory && path != "",
	}
	p.startWriteback()
	return p, nil
}
