package memory

import (
	"context"
	"io"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
)

func (m *memoryBucket) Quiesce(ctx context.Context) error { return m.handles.Drain(ctx) }

type trackedWriter struct {
	io.WriteCloser
	finish func()
	once   sync.Once
	err    error
}

func (w *trackedWriter) Close() error {
	w.once.Do(func() { w.err = w.WriteCloser.Close(); w.finish() })
	return w.err
}

type trackedFile struct {
	storage.File
	finish func()
	once   sync.Once
	err    error
}

func (f *trackedFile) Close() error {
	f.once.Do(func() { f.err = f.File.Close(); f.finish() })
	return f.err
}
