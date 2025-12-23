package pebble

import (
	"context"
	"errors"

	"github.com/cockroachdb/pebble/v2"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/storage/indexdb"
)

var _ storage.IndexDB = (*PebbleDB)(nil)

type PebbleDB struct {
	codec         encoding.Codec
	db            *pebble.DB
	skipErrRecord bool
}

func init() {
	indexdb.Register("pebble", New)
}

// Get implements storage.IndexDB.
func (p *PebbleDB) Get(ctx context.Context, key []byte) (*object.Metadata, error) {
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

	return p.db.Set(key, buf, pebble.Sync)
}

// Iterate implements storage.IndexDB.
func (p *PebbleDB) Iterate(ctx context.Context, prefix []byte, f storage.IterateFunc) error {
	iter, err := p.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return err
	}

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
	return p.db.Delete(key, pebble.Sync)
}

// Exist implements storage.IndexDB.
func (p *PebbleDB) Exist(ctx context.Context, key []byte) bool {
	_, _, err := p.db.Get(key)
	return err == nil
}

// Expired implements storage.IndexDB.
func (p *PebbleDB) Expired(ctx context.Context, f storage.IterateFunc) error {
	panic("unimplemented")
}

// Close implements storage.IndexDB.
func (p *PebbleDB) Close() error {
	return p.db.Close()
}

// GC implements storage.IndexDB.
func (p *PebbleDB) GC(ctx context.Context) error {
	return nil
}

func New(path string, option storage.Option) (storage.IndexDB, error) {
	pdb, err := pebble.Open(path, &pebble.Options{
		Logger: log.NewHelper(log.NewFilter(log.GetLogger(), log.FilterLevel(log.LevelWarn))),
	})
	if err != nil {
		return nil, err
	}

	return &PebbleDB{
		codec:         option.Codec(),
		db:            pdb,
		skipErrRecord: true,
	}, nil
}
