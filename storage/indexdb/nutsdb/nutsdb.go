package nutsdb

import (
	"context"

	"github.com/nutsdb/nutsdb"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/storage/indexdb"
)

var _ storage.IndexDB = (*NutsDB)(nil)

type dbOptions struct {
	// add options here in the future
}

type NutsDB struct {
	codec  encoding.Codec
	db     *nutsdb.DB
	bucket string
}

func init() {
	indexdb.Register("nutsdb", NewNutsDB)
}

func NewNutsDB(path string, option storage.Option) (storage.IndexDB, error) {
	var opts dbOptions
	if err := option.Unmarshal(&opts); err != nil {
		return nil, err
	}

	db, err := nutsdb.Open(
		nutsdb.DefaultOptions,
		nutsdb.WithDir("/tmp/nutsdb"),
	)
	if err != nil {
		return nil, err
	}

	n := &NutsDB{
		codec:  option.Codec(),
		db:     db,
		bucket: "",
	}
	return n, nil
}

// Close implements [storage.IndexDB].
func (n *NutsDB) Close() error {
	return n.db.Close()
}

// Delete implements [storage.IndexDB].
func (n *NutsDB) Delete(ctx context.Context, key []byte) error {
	return n.db.Update(func(tx *nutsdb.Tx) error {
		return tx.Delete(n.bucket, key)
	})
}

// Exist implements [storage.IndexDB].
func (n *NutsDB) Exist(ctx context.Context, key []byte) bool {
	var ret bool
	if err := n.db.View(func(tx *nutsdb.Tx) error {
		v, err := tx.Get(n.bucket, key)
		if err != nil {
			return err
		}
		if v != nil {
			ret = true
		}
		return nil
	}); err != nil {
		return false
	}
	return ret
}

// Expired implements [storage.IndexDB].
func (n *NutsDB) Expired(ctx context.Context, f storage.IterateFunc) error {
	return nil
}

// GC implements [storage.IndexDB].
func (n *NutsDB) GC(ctx context.Context) error {
	return nil
}

// Get implements [storage.IndexDB].
func (n *NutsDB) Get(ctx context.Context, key []byte) (*object.Metadata, error) {
	var meta object.Metadata
	if err := n.db.View(func(tx *nutsdb.Tx) error {
		v, err := tx.Get(n.bucket, key)
		if err != nil {
			return err
		}
		if v != nil {
			return n.codec.Unmarshal(v, &meta)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &meta, nil
}

// Iterate implements [storage.IndexDB].
func (n *NutsDB) Iterate(ctx context.Context, prefix []byte, f storage.IterateFunc) error {
	tx, err := n.db.Begin(false)
	if err != nil {
		return err
	}

	iterator := nutsdb.NewIterator(tx, n.bucket, nutsdb.IteratorOptions{Reverse: false})
	if iterator == nil {
		return tx.Commit()
	}

	load := func(key, val []byte) error {
		meta := &object.Metadata{}
		if err = n.codec.Unmarshal(val, meta); err != nil {
			return err
		}
		f(key, meta)
		return nil
	}

	if len(prefix) == 0 {
		for iterator.Valid(); iterator.Next(); {
			buf, err := iterator.Value()
			if err != nil {
				_ = err
				continue
			}

			if err := load(iterator.Key(), buf); err != nil {
				_ = err
				continue
			}
		}
		return tx.Commit()
	}

	for iterator.Seek(prefix); iterator.Valid(); iterator.Next() {
		buf, err := iterator.Value()
		if err != nil {
			_ = err
			continue
		}

		if err := load(iterator.Key(), buf); err != nil {
			continue
		}
	}

	return tx.Commit()
}

// Set implements [storage.IndexDB].
func (n *NutsDB) Set(ctx context.Context, key []byte, val *object.Metadata) error {
	return n.db.Update(func(tx *nutsdb.Tx) error {
		buf, err := n.codec.Marshal(val)
		if err != nil {
			return err
		}
		return tx.Put(n.bucket, key, buf, 0)
	})
}
