package memory

import (
	"context"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var _ storage.Bucket = (*memoryBucket)(nil)

type memoryBucket struct{}

func (r *memoryBucket) Objects() uint64 {
	return 0
}

func New() (storage.Bucket, error) {
	return &memoryBucket{}, nil
}

func (r *memoryBucket) Close() error {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Store(ctx context.Context, meta *object.Metadata) error {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Exist(ctx context.Context, id []byte) bool {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Remove(ctx context.Context, id *object.ID) error {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Discard(ctx context.Context, id *object.ID) error {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) DiscardWithHash(ctx context.Context, hash object.IDHash) error {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Iterate(ctx context.Context, fn func(*object.Metadata) error) error {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) ID() string {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Weight() int {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Allow() int {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) UseAllow() bool {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) HasBad() bool {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Type() string {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) StoreType() string {
	//TODO implement me
	panic("implement me")
}

func (r *memoryBucket) Path() string {
	//TODO implement me
	panic("implement me")
}
