package caching

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

type leasedBucket struct {
	storage.Bucket
	lease storage.LegacyFillLease
}

var _ storage.Bucket = (*leasedBucket)(nil)

func (b *leasedBucket) Store(ctx context.Context, md *object.Metadata) error {
	return b.lease.Guard(ctx, func() error { return b.Bucket.Store(ctx, md) })
}
func (b *leasedBucket) Remove(ctx context.Context, id *object.ID) error {
	return b.lease.Guard(ctx, func() error { return b.Bucket.Remove(ctx, id) })
}
func (b *leasedBucket) Discard(ctx context.Context, id *object.ID) error {
	return b.lease.Guard(ctx, func() error { return b.Bucket.Discard(ctx, id) })
}
func (b *leasedBucket) DiscardWithHash(ctx context.Context, id object.IDHash) error {
	return b.lease.Guard(ctx, func() error { return b.Bucket.DiscardWithHash(ctx, id) })
}
func (b *leasedBucket) DiscardWithMessage(ctx context.Context, id *object.ID, message string) error {
	return b.lease.Guard(ctx, func() error { return b.Bucket.DiscardWithMessage(ctx, id, message) })
}
func (b *leasedBucket) DiscardWithMetadata(ctx context.Context, md *object.Metadata) error {
	return b.lease.Guard(ctx, func() error { return b.Bucket.DiscardWithMetadata(ctx, md) })
}

func (c *Caching) attachLegacyLease(lease storage.LegacyFillLease) {
	if bucket, ok := c.bucket.(*leasedBucket); ok {
		c.bucket = bucket.Bucket
	}
	c.legacyLease = lease
	c.bucket = &leasedBucket{Bucket: c.bucket, lease: lease}
	c.legacyLeaseStop = context.AfterFunc(c.req.Context(), func() { lease.Close() })
}

func (c *Caching) finishLegacyLease(resp *http.Response) {
	if c.legacyLease == nil {
		return
	}
	lease, stop := c.legacyLease, c.legacyLeaseStop
	close := func() {
		if stop != nil {
			stop()
		}
		lease.Close()
	}
	if resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		close()
		return
	}
	resp.Body = &leaseBody{ReadCloser: resp.Body, finish: close}
}

type leaseBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

var _ io.ReadCloser = (*leaseBody)(nil)

func (b *leaseBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.finish); return err }
