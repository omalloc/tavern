package empty

import (
	"context"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
)

var _ storage.Bucket = (*emptyBucket)(nil)

type emptyBucket struct {
	path string
}

// Allow implements storage.Bucket.
func (e *emptyBucket) Allow() int {
	return 100
}

// Close implements storage.Bucket.
func (e *emptyBucket) Close() error {
	return nil
}

// Discard implements storage.Bucket.
func (e *emptyBucket) Discard(ctx context.Context, id *object.ID) error {
	return nil
}

// DiscardWithHash implements storage.Bucket.
func (e *emptyBucket) DiscardWithHash(ctx context.Context, hash object.IDHash) error {
	return nil
}

// DiscardWithMessage implements storage.Bucket.
func (e *emptyBucket) DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error {
	return nil
}

// DiscardWithMetadata implements storage.Bucket.
func (e *emptyBucket) DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error {
	return nil
}

// Exist implements storage.Bucket.
func (e *emptyBucket) Exist(ctx context.Context, id []byte) bool {
	return false
}

// Expired implements storage.Bucket.
func (e *emptyBucket) Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool {
	return true
}

// HasBad implements storage.Bucket.
func (e *emptyBucket) HasBad() bool {
	return false
}

// ID implements storage.Bucket.
func (e *emptyBucket) ID() string {
	return "empty:/dev/null"
}

// Iterate implements storage.Bucket.
func (e *emptyBucket) Iterate(ctx context.Context, fn func(*object.Metadata) error) error {
	return nil
}

// Lookup implements storage.Bucket.
func (e *emptyBucket) Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	return nil, storage.ErrKeyNotFound
}

// Remove implements storage.Bucket.
func (e *emptyBucket) Remove(ctx context.Context, id *object.ID) error {
	return nil
}

// Store implements storage.Bucket.
func (e *emptyBucket) Store(ctx context.Context, meta *object.Metadata) error {
	return nil
}

// UseAllow implements storage.Bucket.
func (e *emptyBucket) UseAllow() bool {
	return true
}

// Weight implements storage.Bucket.
func (e *emptyBucket) Weight() int {
	return 1000
}

// StoreType implements storage.Bucket.
func (e *emptyBucket) StoreType() string {
	return "empty"
}

// Type implements storage.Bucket.
func (e *emptyBucket) Type() string {
	return "empty"
}

func (e *emptyBucket) Path() string {
	return e.path
}

func New(c *conf.Bucket, _ storage.SharedKV) (storage.Bucket, error) {
	path := c.Path
	if path == "" {
		path = "/dev/null"
	}
	return &emptyBucket{
		path: path,
	}, nil
}
