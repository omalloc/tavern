package caching

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/representation"
)

// representationWriter owns a fixed ID, bucket and metadata snapshot. Network
// reads happen outside its mutex. Only confirmed closed chunks become readable;
// no reference is published until exact length and EOF have been observed.
type representationWriter struct {
	body     io.ReadCloser
	ctx      context.Context
	bucket   storage.Bucket
	repo     *representation.IndexRepository
	state    *representation.IndexState
	lease    *representation.Reservation
	ref      object.VariantRef
	previous *object.VariantRef
	md       *object.Metadata
	mu       sync.Mutex
	buffer   []byte
	total    uint64
	index    uint32
	terminal bool
	stop     func() bool
	closed   sync.Once
	closeErr error
	onError  func(error)
}

func newRepresentationWriter(body io.ReadCloser, ctx context.Context, bucket storage.Bucket, repo *representation.IndexRepository, state *representation.IndexState, lease *representation.Reservation, ref object.VariantRef, previous *object.VariantRef, md *object.Metadata, onError func(error)) io.ReadCloser {
	w := &representationWriter{body: body, ctx: ctx, bucket: bucket, repo: repo, state: state, lease: lease,
		ref: ref.Clone(), md: md.Clone(), onError: onError}
	if previous != nil {
		copy := previous.Clone()
		w.previous = &copy
	}
	w.mu.Lock()
	w.stop = context.AfterFunc(ctx, func() { w.mu.Lock(); defer w.mu.Unlock(); w.abortLocked() })
	w.mu.Unlock()
	return w
}

func (w *representationWriter) Read(p []byte) (int, error) {
	n, err := w.body.Read(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.terminal {
		return n, err
	}
	if w.ctx.Err() != nil {
		w.abortLocked()
		return n, err
	}
	if n > 0 {
		if uint64(n) > w.md.Size-w.total {
			w.failLocked(io.ErrUnexpectedEOF)
			return n, err
		}
		w.total += uint64(n)
		remaining := p[:n]
		for len(remaining) > 0 {
			take := min(uint64(len(remaining)), w.md.BlockSize-uint64(len(w.buffer)))
			w.buffer = append(w.buffer, remaining[:int(take)]...)
			remaining = remaining[int(take):]
			if uint64(len(w.buffer)) == w.md.BlockSize {
				if failure := w.flushLocked(); failure != nil {
					w.failLocked(failure)
					return n, err
				}
			}
		}
	}
	if errors.Is(err, io.EOF) {
		if w.total != w.md.Size {
			w.failLocked(io.ErrUnexpectedEOF)
			return n, err
		}
		if failure := w.flushLocked(); failure != nil {
			w.failLocked(failure)
			return n, err
		}
		w.md.Representation.State = object.RepresentationComplete
		published, failure := w.repo.Publish(w.ctx, w.state, w.lease, w.ref, w.md, w.previous, func() error { return w.bucket.Store(w.ctx, w.md) })
		if failure != nil {
			w.failLocked(failure)
			return n, err
		}
		if !published {
			w.abortLocked()
			return n, err
		}
		w.terminal = true
		w.stop()
		w.buffer = nil
	} else if err != nil {
		w.failLocked(err)
	}
	return n, err
}

func (w *representationWriter) flushLocked() error {
	if len(w.buffer) == 0 {
		return nil
	}
	// Parts describes cleanup ownership of a file being written. Chunks
	// describes only successfully written and closed files.
	w.md.Parts.Set(w.index)
	if err := w.bucket.Store(w.ctx, w.md); err != nil {
		return err
	}
	writer, _, err := w.bucket.WriteChunkFile(w.ctx, w.md.ID, w.index)
	if err != nil {
		return err
	}
	n, writeErr := writer.Write(w.buffer)
	closeErr := writer.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(w.buffer) {
		return io.ErrShortWrite
	}
	if closeErr != nil {
		return closeErr
	}
	w.md.Parts.Remove(w.index)
	w.md.Chunks.Set(w.index)
	w.index++
	w.buffer = w.buffer[:0]
	return w.bucket.Store(w.ctx, w.md)
}

func (w *representationWriter) failLocked(err error) {
	w.abortLocked()
	if w.onError != nil {
		w.onError(err)
	}
}

func (w *representationWriter) abortLocked() {
	if w.terminal {
		return
	}
	w.terminal = true
	w.buffer = nil
	w.repo.Release(w.lease)
	// Cleanup must survive request cancellation. This ID belongs only to
	// this unpublished generation; no previously published ID is removed.
	_ = w.bucket.Discard(context.WithoutCancel(w.ctx), w.md.ID)
	if w.stop != nil {
		w.stop()
	}
}

func (w *representationWriter) Close() error {
	w.closed.Do(func() {
		w.closeErr = w.body.Close()
		w.mu.Lock()
		defer w.mu.Unlock()
		w.abortLocked()
	})
	return w.closeErr
}
