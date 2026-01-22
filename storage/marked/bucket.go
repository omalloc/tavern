package marked

import (
	"context"
	"io"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

type wrappedBucket struct {
	base    storagev1.Bucket
	checker Checker
}

func wrapBucket(base storagev1.Bucket, checker Checker) storagev1.Bucket {
	if checker == nil {
		return base
	}
	return &wrappedBucket{base: base, checker: checker}
}

func (b *wrappedBucket) Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	md, err := b.base.Lookup(ctx, id)
	if err != nil || md == nil {
		return md, err
	}

	marked, err := b.checker.Marked(ctx, id, md)
	if err != nil {
		return md, nil
	}
	if !marked {
		return md, nil
	}

	// Mark as expired for this lookup by setting ExpiresAt into the past.
	// NOTE: We intentionally do not persist this change via Store here to avoid
	// additional writes during Lookup; callers should treat the returned metadata
	// as expired.
	md.ExpiresAt = time.Now().Add(-1 * time.Second).Unix()
	return md, nil
}

func (b *wrappedBucket) Touch(ctx context.Context, id *object.ID) error {
	return b.base.Touch(ctx, id)
}

func (b *wrappedBucket) Store(ctx context.Context, meta *object.Metadata) error {
	return b.base.Store(ctx, meta)
}

func (b *wrappedBucket) Exist(ctx context.Context, id []byte) bool {
	return b.base.Exist(ctx, id)
}

func (b *wrappedBucket) Remove(ctx context.Context, id *object.ID) error {
	return b.base.Remove(ctx, id)
}

func (b *wrappedBucket) Discard(ctx context.Context, id *object.ID) error {
	return b.base.Discard(ctx, id)
}

func (b *wrappedBucket) DiscardWithHash(ctx context.Context, hash object.IDHash) error {
	return b.base.DiscardWithHash(ctx, hash)
}

func (b *wrappedBucket) DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error {
	return b.base.DiscardWithMessage(ctx, id, msg)
}

func (b *wrappedBucket) DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error {
	return b.base.DiscardWithMetadata(ctx, meta)
}

func (b *wrappedBucket) Iterate(ctx context.Context, fn func(*object.Metadata) error) error {
	return b.base.Iterate(ctx, fn)
}

func (b *wrappedBucket) Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool {
	return b.base.Expired(ctx, id, md)
}

func (b *wrappedBucket) WriteChunkFile(ctx context.Context, id *object.ID, index uint32) (io.WriteCloser, string, error) {
	return b.base.WriteChunkFile(ctx, id, index)
}

func (b *wrappedBucket) ReadChunkFile(ctx context.Context, id *object.ID, index uint32) (storagev1.File, string, error) {
	return b.base.ReadChunkFile(ctx, id, index)
}

func (b *wrappedBucket) ID() string {
	return b.base.ID()
}

func (b *wrappedBucket) Weight() int {
	return b.base.Weight()
}

func (b *wrappedBucket) Allow() int {
	return b.base.Allow()
}

func (b *wrappedBucket) UseAllow() bool {
	return b.base.UseAllow()
}

func (b *wrappedBucket) Objects() uint64 {
	return b.base.Objects()
}

func (b *wrappedBucket) HasBad() bool {
	return b.base.HasBad()
}

func (b *wrappedBucket) Type() string {
	return b.base.Type()
}

func (b *wrappedBucket) StoreType() string {
	return b.base.StoreType()
}

func (b *wrappedBucket) Path() string {
	return b.base.Path()
}

func (b *wrappedBucket) Close() error {
	return b.base.Close()
}
