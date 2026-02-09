package sharedkv

import (
	"context"
	"encoding/binary"
	"errors"

	"github.com/cockroachdb/pebble/v2"
	"github.com/omalloc/tavern/api/defined/v1/storage"
)

var _ storage.SharedKV = (*noneSharedKV)(nil)

type noneSharedKV struct {
	db *pebble.DB
}

func (r *noneSharedKV) Close() error {
	return r.db.Close()
}

func (r *noneSharedKV) Get(_ context.Context, key []byte) ([]byte, error) {
	val, c, err := r.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, storage.ErrKeyNotFound
		}
		return nil, err
	}

	defer func() { _ = c.Close() }()

	return val, nil
}

func (r *noneSharedKV) Set(_ context.Context, key []byte, val []byte) error {
	return r.db.Set(key, val, pebble.NoSync)
}

func (r *noneSharedKV) Incr(_ context.Context, key []byte, delta uint32) (uint32, error) {
	batch := r.db.NewIndexedBatch()
	defer func() { _ = batch.Close() }()

	val, closer, err := batch.Get(key)
	if err != nil {
		// set zero
		_ = err
		val = make([]byte, 4)
	} else {
		defer func() { _ = closer.Close() }()
	}

	counter := binary.BigEndian.Uint32(val)

	counter += delta

	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, counter)

	if err1 := batch.Set(key, buf, pebble.NoSync); err1 != nil {
		return 0, err1
	}

	if err1 := batch.Commit(pebble.NoSync); err1 != nil {
		return 0, err1
	}

	return counter, nil
}

func (r *noneSharedKV) Decr(_ context.Context, key []byte, delta uint32) (uint32, error) {
	batch := r.db.NewIndexedBatch()
	defer func() { _ = batch.Close() }()

	val, closer, err := batch.Get(key)
	if err != nil {
		// set zero
		_ = err
		val = make([]byte, 4)
	} else {
		defer func() { _ = closer.Close() }()
	}

	counter := binary.BigEndian.Uint32(val)

	if counter > delta {
		counter -= delta
	} else {
		counter = 0
	}

	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, counter)

	if err1 := batch.Set(key, buf, pebble.NoSync); err1 != nil {
		return 0, err1
	}

	if err1 := batch.Commit(pebble.NoSync); err1 != nil {
		return 0, err1
	}

	return counter, nil
}

func (r *noneSharedKV) GetCounter(_ context.Context, key []byte) (uint32, error) {
	val, closer, err := r.db.Get(key)
	if err != nil {
		return 0, err
	}
	defer func() { _ = closer.Close() }()

	return binary.BigEndian.Uint32(val), nil
}

func (r *noneSharedKV) Delete(_ context.Context, key []byte) error {
	return r.db.Delete(key, pebble.NoSync)
}

func (r *noneSharedKV) DropPrefix(ctx context.Context, prefix []byte) error {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	end[len(end)-1]++

	return r.db.DeleteRange(prefix, end, pebble.NoSync)
}

func (r *noneSharedKV) Iterate(ctx context.Context, f func(key []byte, val []byte) error) error {
	iter, err := r.db.NewIterWithContext(ctx, &pebble.IterOptions{})
	if err != nil {
		return err
	}

	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		value, err1 := iter.ValueAndErr()
		if err1 != nil {
			continue
		}
		if err1 = f(iter.Key(), value); err1 != nil {
			continue
		}
	}

	return nil
}

func (r *noneSharedKV) IteratePrefix(ctx context.Context, prefix []byte, f func(key []byte, val []byte) error) error {
	iter, err := r.db.NewIterWithContext(ctx, &pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: keyUpperBound(prefix),
	})
	if err != nil {
		return err
	}

	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		value, err1 := iter.ValueAndErr()
		if err1 != nil {
			continue
		}
		if err1 = f(iter.Key(), value); err1 != nil {
			continue
		}
	}

	return nil
}

func keyUpperBound(b []byte) []byte {
	end := make([]byte, len(b))
	copy(end, b)
	for i := len(end) - 1; i >= 0; i-- {
		end[i] = end[i] + 1
		if end[i] != 0 {
			return end[:i+1]
		}
	}
	return nil // no upper-bound
}

func newNoneKV(storePath string, opts *pebble.Options) (storage.SharedKV, error) {
	db, err := pebble.Open(storePath, opts)
	if err != nil {
		return nil, err
	}

	r := &noneSharedKV{
		db: db,
	}
	return r, nil
}
