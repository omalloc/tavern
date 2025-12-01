package empty

import (
	"context"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
)

var _ storage.Bucket = (*emptyBucket)(nil)

type emptyBucket struct{}

// Allow implements storage.Bucket.
func (e *emptyBucket) Allow() int {
	panic("unimplemented")
}

// Close implements storage.Bucket.
func (e *emptyBucket) Close() error {
	panic("unimplemented")
}

// Discard implements storage.Bucket.
func (e *emptyBucket) Discard(ctx context.Context, id *object.ID) error {
	panic("unimplemented")
}

// DiscardWithHash implements storage.Bucket.
func (e *emptyBucket) DiscardWithHash(ctx context.Context, hash object.IDHash) error {
	panic("unimplemented")
}

// DiscardWithMessage implements storage.Bucket.
func (e *emptyBucket) DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error {
	panic("unimplemented")
}

// DiscardWithMetadata implements storage.Bucket.
func (e *emptyBucket) DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error {
	panic("unimplemented")
}

// Exist implements storage.Bucket.
func (e *emptyBucket) Exist(ctx context.Context, id []byte) bool {
	panic("unimplemented")
}

// Expired implements storage.Bucket.
func (e *emptyBucket) Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool {
	panic("unimplemented")
}

// HasBad implements storage.Bucket.
func (e *emptyBucket) HasBad() bool {
	panic("unimplemented")
}

// ID implements storage.Bucket.
func (e *emptyBucket) ID() string {
	panic("unimplemented")
}

// Iterate implements storage.Bucket.
func (e *emptyBucket) Iterate(ctx context.Context, fn func(*object.Metadata) error) error {
	panic("unimplemented")
}

// Lookup implements storage.Bucket.
func (e *emptyBucket) Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	panic("unimplemented")
}

// Remove implements storage.Bucket.
func (e *emptyBucket) Remove(ctx context.Context, id *object.ID) error {
	panic("unimplemented")
}

// Store implements storage.Bucket.
func (e *emptyBucket) Store(ctx context.Context, meta *object.Metadata) error {
	panic("unimplemented")
}

// UseAllow implements storage.Bucket.
func (e *emptyBucket) UseAllow() bool {
	panic("unimplemented")
}

// Weight implements storage.Bucket.
func (e *emptyBucket) Weight() int {
	panic("unimplemented")
}

func New(_ *conf.Bucket, _ storage.SharedKV) (storage.Bucket, error) {
	return &emptyBucket{}, nil
}
