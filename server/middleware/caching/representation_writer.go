package caching

import (
	"context"
	"errors"
	"io"
	"math"
	"strconv"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/representation"
)

// representationWriter owns a fixed ID, bucket and metadata snapshot. Network
// reads happen outside its mutex. Only confirmed closed chunks become readable;
// no reference is published until the origin response reaches EOF. Partial
// responses publish only whole chunks in their own immutable generation.
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
	span     representationSpan
	terminal bool
	stop     func() bool
	closed   sync.Once
	closeErr error
	onError  func(error)
}

var _ io.ReadCloser = (*representationWriter)(nil)

func newRepresentationWriter(body io.ReadCloser, ctx context.Context, bucket storage.Bucket, repo *representation.IndexRepository, state *representation.IndexState, lease *representation.Reservation, ref object.VariantRef, previous *object.VariantRef, md *object.Metadata, span representationSpan, onError func(error)) io.ReadCloser {
	w := &representationWriter{body: body, ctx: ctx, bucket: bucket, repo: repo, state: state, lease: lease,
		ref: ref.Clone(), md: md.Clone(), span: span, onError: onError}
	if previous != nil {
		copy := previous.Clone()
		w.previous = &copy
	}
	w.mu.Lock()
	w.stop = context.AfterFunc(ctx, func() {
		w.mu.Lock()
		w.abortLocked()
		w.mu.Unlock()
		// Closing the upstream body interrupts a pending network Read. Never
		// hold the session lock while calling an external Body.Close.
		_ = w.closeBody()
	})
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
		if uint64(n) > math.MaxInt64-w.span.start-w.total || (!w.span.unknown && uint64(n) > w.span.length-w.total) {
			w.failLocked(io.ErrUnexpectedEOF)
			return n, err
		}
		remaining := p[:n]
		for len(remaining) > 0 {
			position := w.span.start + w.total
			offset := position % w.md.BlockSize
			take := min(uint64(len(remaining)), w.md.BlockSize-offset)
			// A response beginning inside a chunk cannot establish its prefix.
			// Discard those leading bytes, preserving the original wire body.
			if offset != 0 && len(w.buffer) == 0 {
				w.total += take
				remaining = remaining[int(take):]
				continue
			}
			if position/w.md.BlockSize > math.MaxUint32 {
				w.failLocked(errors.New("representation exceeds chunk index capacity"))
				return n, err
			}
			w.buffer = append(w.buffer, remaining[:int(take)]...)
			w.total += take
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
		if !w.span.unknown && w.total != w.span.length {
			w.failLocked(io.ErrUnexpectedEOF)
			return n, err
		}
		if w.span.unknown {
			w.md.Size = w.total
			w.md.Headers.Set("Content-Length", strconv.FormatUint(w.total, 10))
		}
		// A trailing fragment is readable only when it reaches the known
		// object end. Other incomplete boundary chunks are never confirmed.
		if w.span.start+w.total != w.md.Size {
			w.buffer = nil
		}
		if failure := w.flushLocked(); failure != nil {
			w.failLocked(failure)
			return n, err
		}
		w.md.Representation.State = object.RepresentationComplete
		if !w.md.HasComplete() {
			w.md.Representation.State = object.RepresentationFilling
		}
		if !w.md.HasReadableChunks() {
			w.abortLocked()
			return n, err
		}
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
	index := uint32((w.span.start + w.total - uint64(len(w.buffer))) / w.md.BlockSize)
	// Parts describes cleanup ownership of a file being written. Chunks
	// describes only successfully written and closed files.
	w.md.Parts.Set(index)
	if err := w.bucket.Store(w.ctx, w.md); err != nil {
		return err
	}
	writer, _, err := w.bucket.WriteChunkFile(w.ctx, w.md.ID, index)
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
	w.md.Parts.Remove(index)
	w.md.Chunks.Set(index)
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
	err := w.closeBody()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.abortLocked()
	return err
}

func (w *representationWriter) closeBody() error {
	w.closed.Do(func() {
		w.closeErr = w.body.Close()
	})
	return w.closeErr
}
