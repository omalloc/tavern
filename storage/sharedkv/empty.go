package sharedkv

import (
	"context"

	"github.com/omalloc/tavern/api/defined/v1/storage"
)

var _ storage.SharedKV = (*emptySharedKV)(nil)

type emptySharedKV struct{}

// Close implements storage.SharedKV.
func (e *emptySharedKV) Close() error {
	return nil
}

// Decr implements storage.SharedKV.
func (e *emptySharedKV) Decr(ctx context.Context, key []byte, delta uint32) (uint32, error) {
	return 0, nil
}

// Delete implements storage.SharedKV.
func (e *emptySharedKV) Delete(ctx context.Context, key []byte) error {
	return nil
}

// DropPrefix implements storage.SharedKV.
func (e *emptySharedKV) DropPrefix(ctx context.Context, prefix []byte) error {
	return nil
}

// Get implements storage.SharedKV.
func (e *emptySharedKV) Get(ctx context.Context, key []byte) ([]byte, error) {
	return nil, storage.ErrSharedKVKeyNotFound
}

// GetCounter implements storage.SharedKV.
func (e *emptySharedKV) GetCounter(ctx context.Context, key []byte) (uint32, error) {
	return 0, storage.ErrSharedKVKeyNotFound
}

// Incr implements storage.SharedKV.
func (e *emptySharedKV) Incr(ctx context.Context, key []byte, delta uint32) (uint32, error) {
	return 0, storage.ErrSharedKVKeyNotFound
}

// Iterate implements storage.SharedKV.
func (e *emptySharedKV) Iterate(ctx context.Context, f func(key []byte, val []byte) error) error {
	return nil
}

// IteratePrefix implements storage.SharedKV.
func (e *emptySharedKV) IteratePrefix(ctx context.Context, prefix []byte, f func(key []byte, val []byte) error) error {
	return nil
}

// Set implements storage.SharedKV.
func (e *emptySharedKV) Set(ctx context.Context, key []byte, val []byte) error {
	return nil
}

func NewEmpty() storage.SharedKV {
	return &emptySharedKV{}
}
