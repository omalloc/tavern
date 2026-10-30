package caching

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

func TestLegacyCancellationKeepsLeaseUntilActualBodyClose(t *testing.T) {
	f, store := registeredOriginFixture(t, func(http.ResponseWriter, *http.Request) {})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := store.(storage.LegacyFillCoordinator).BeginLegacyFill(ctx, f.target)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	c := &Caching{req: varyRequest(t, f.target, nil).WithContext(ctx), bucket: f.bucket}
	c.attachLegacyLease(lease)
	body := &sharingBlockedBody{entered: make(chan struct{}), closeStarted: make(chan struct{}), finishClose: make(chan struct{})}
	var release = make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(body.finishClose)
		}
	}()
	resp := &http.Response{Body: body}
	c.finishLegacyLease(resp)
	cancel()
	select {
	case <-body.closeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not close legacy body")
	}
	if err := lease.Guard(context.Background(), func() error { return nil }); err != nil {
		t.Fatal("cancellation released physical exclusion before close", err)
	}
	close(body.finishClose)
	close(release)
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Guard(context.Background(), func() error { return nil }); !errors.Is(err, storage.ErrFillSuperseded) {
		t.Fatal("closed body retained legacy exclusion", err)
	}
}

type queuedLegacyBucket struct {
	storage.Bucket
	entered chan context.Context
	proceed chan struct{}
}

func (b *queuedLegacyBucket) WriteChunkFile(ctx context.Context, id *object.ID, index uint32) (io.WriteCloser, string, error) {
	b.entered <- ctx
	<-b.proceed
	return b.Bucket.WriteChunkFile(ctx, id, index)
}

func TestLegacyQueuedWriteOwnsBoundedLifecycle(t *testing.T) {
	f, store := registeredOriginFixture(t, func(http.ResponseWriter, *http.Request) {})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := store.(storage.LegacyFillCoordinator).BeginLegacyFill(ctx, f.target)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	bucket := &queuedLegacyBucket{Bucket: f.bucket, entered: make(chan context.Context, 1), proceed: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(bucket.proceed) }) }
	defer unblock()
	md := &object.Metadata{ID: object.NewID(f.target), Size: 4, BlockSize: 4, Code: 200, Headers: http.Header{"Content-Length": {"4"}}}
	c := &Caching{req: varyRequest(t, f.target, nil).WithContext(ctx), bucket: bucket, md: md, taskStore: store, opt: &cachingOption{AsyncFlushChunk: true}, log: log.NewHelper(log.GetLogger())}
	c.attachLegacyLease(lease)
	session := c.newWriteSession()
	write, finish := flushbufferSlice(session, xhttp.ContentRange{ObjSize: 4})
	done := make(chan error, 1)
	go func() { err := write([]byte("body"), 0, 4, true); finish(true); done <- err }()
	writeCtx := <-bucket.entered
	cancel()
	if writeCtx.Err() != nil {
		t.Fatal("client completion canceled accepted local bytes", writeCtx.Err())
	}
	if _, bounded := writeCtx.Deadline(); !bounded {
		t.Fatal("local write has no deadline")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stored, err := f.bucket.Lookup(t.Context(), md.ID)
	if err != nil || !stored.HasComplete() {
		t.Fatal("queued complete block was lost", err)
	}
	if writeCtx.Err() == nil {
		t.Fatal("finished write task retained its context")
	}
}
