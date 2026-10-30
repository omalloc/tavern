package caching

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/omalloc/tavern/storage/representation"
)

// Close underlying lazy chunk readers before releasing their physical pin.
type representationLeaseBody struct {
	io.ReadCloser
	lease *representation.ReadLease
	once  sync.Once
	ready sync.Mutex
	stop  func() bool
	err   error
}

func (b *representationLeaseBody) Close() error {
	b.once.Do(func() {
		b.err = b.ReadCloser.Close()
		b.ready.Lock()
		if b.stop != nil {
			b.stop()
		}
		b.ready.Unlock()
		b.lease.Close()
	})
	return b.err
}

func (c *Caching) finishRepresentationLease(resp *http.Response) {
	if c.representationLease == nil {
		return
	}
	if resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		c.representationLease.Close()
		return
	}
	body := &representationLeaseBody{ReadCloser: resp.Body, lease: c.representationLease}
	body.ready.Lock()
	body.stop = context.AfterFunc(c.req.Context(), func() { body.Close() })
	body.ready.Unlock()
	resp.Body = body
}
