package caching

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/event"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/iobuf"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

// writeSession is the legacy Processor adapter's fixed write target. The async
// worker owns its metadata clone; callbacks never consult the frontend Caching
// object. V2 uses representationWriter with an independently allocated body ID.
type writeSession struct {
	ctx      context.Context
	bucket   storage.Bucket
	md       *object.Metadata
	log      *log.Helper
	async    bool
	publish  func(context.Context, event.CacheCompleted)
	failed   bool
	disabled bool
	mu       sync.Mutex
	lease    storage.LegacyFillLease
	finish   func()
}

func (c *Caching) newWriteSession() *writeSession {
	s := &writeSession{ctx: c.req.Context(), bucket: c.bucket, md: c.md.Clone(), log: c.log,
		async: c.opt.AsyncFlushChunk, publish: c.opt.publish}
	// Queued local bytes already belong to an accepted task. A client that
	// finishes its advertised range must not cancel those writes before Close
	// joins them; storage Drain still cancels and waits for this bounded owner.
	ctx, finish, err := beginCacheTask(c.taskStore, context.WithoutCancel(c.req.Context()), defaultTaskTimeout)
	if err != nil {
		s.disabled, s.finish = true, func() {}
	} else {
		s.ctx, s.finish = ctx, finish
	}
	if bucket, ok := c.bucket.(*leasedBucket); ok {
		s.bucket, s.lease = bucket.Bucket, bucket.lease
	}
	return s
}

func (s *writeSession) guard(write func() error) error {
	if s.lease != nil {
		return s.lease.Guard(s.ctx, write)
	}
	return write()
}

func flushbufferSlice(s *writeSession, span xhttp.ContentRange) (iobuf.EventSuccess, iobuf.EventClose) {
	chunked := span.ObjSize == 0
	writeLocal := func(buf []byte, index uint32, current uint64, eof bool) error {
		if s.failed {
			return io.ErrClosedPipe
		}
		if !chunked && uint64(len(buf)) != s.md.BlockSize && current != span.ObjSize {
			return nil
		}
		f, path, err := s.bucket.WriteChunkFile(s.ctx, s.md.ID, index)
		if err != nil {
			return err
		}
		n, writeErr := f.Write(buf)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if n != len(buf) {
			return io.ErrShortWrite
		}
		if closeErr != nil {
			return closeErr
		}
		// File Close is part of confirming a readable block.
		s.md.Chunks.Set(index)
		if chunked {
			s.md.Size = current
			s.md.Headers.Set("Content-Length", fmt.Sprint(current))
		}
		// Unknown total size cannot be published before EOF. The legacy
		// descriptor has no explicit filling state.
		if !chunked && !s.async {
			if err := s.bucket.Store(s.ctx, s.md); err != nil {
				return err
			}
		}
		cacheChunkWriteTotal.WithLabelValues("success", s.bucket.StoreType()).Inc()
		if eof && s.md.HasComplete() && s.publish != nil && s.bucket.Type() != storage.TypeInMemory {
			s.publish(context.WithoutCancel(s.ctx), &cacheCompleted{
				storeUrl: s.md.ID.Key(), storeKey: s.md.ID.HashStr(), storePath: filepath.Dir(path),
				lastModified: s.md.Headers.Get("Last-Modified"), contentLength: int64(s.md.Size),
				chunkCount: s.md.Chunks.Count(), chunkSize: s.md.BlockSize,
			})
		}
		return nil
	}
	write := func(buf []byte, index uint32, current uint64, eof bool) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.disabled {
			return nil
		}
		err := s.guard(func() error { return writeLocal(buf, index, current, eof) })
		if errors.Is(err, storage.ErrFillSuperseded) {
			s.disabled = true
			return nil
		}
		return err
	}
	close := func(eof bool) {
		defer s.finish()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.failed || s.disabled {
			return
		}
		if chunked && !eof {
			s.failLocked(io.ErrUnexpectedEOF)
			return
		}
		if s.async || chunked {
			if err := s.guard(func() error { return s.bucket.Store(s.ctx, s.md) }); err != nil {
				s.failLocked(err)
			}
		}
	}
	return write, close
}

func (s *writeSession) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failLocked(err)
}

func (s *writeSession) failLocked(err error) {
	if s.failed {
		return
	}
	s.failed = true
	s.log.Errorf("flush body to disk failed: %v", err)
	cacheFlushFailedTotal.WithLabelValues(s.bucket.StoreType()).Inc()
	cacheChunkWriteTotal.WithLabelValues("failed", s.bucket.StoreType()).Inc()
	if s.lease != nil {
		_ = s.lease.Guard(context.WithoutCancel(s.ctx), func() error { return s.bucket.DiscardWithMetadata(context.WithoutCancel(s.ctx), s.md) })
	} else {
		_ = s.bucket.DiscardWithMetadata(context.WithoutCancel(s.ctx), s.md)
	}
}
