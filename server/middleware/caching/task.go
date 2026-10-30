package caching

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
)

const defaultTaskTimeout = 2 * time.Minute

type taskBody struct {
	io.ReadCloser
	once   sync.Once
	ready  sync.Mutex
	stop   func() bool
	finish func()
	err    error
}

func (b *taskBody) Close() error {
	b.once.Do(func() {
		b.err = b.ReadCloser.Close()
		b.ready.Lock()
		if b.stop != nil {
			b.stop()
		}
		b.ready.Unlock()
		b.finish()
	})
	return b.err
}
func finishCacheTask(ctx context.Context, resp *http.Response, finish func()) {
	if finish == nil {
		return
	}
	if resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		finish()
		return
	}
	body := &taskBody{ReadCloser: resp.Body, finish: finish}
	body.ready.Lock()
	body.stop = context.AfterFunc(ctx, func() { body.Close() })
	body.ready.Unlock()
	resp.Body = body
}

func beginCacheTask(store storage.Storage, ctx context.Context, timeout time.Duration) (context.Context, func(), error) {
	if coordinator, ok := store.(storage.TaskCoordinator); ok {
		return coordinator.BeginCacheTask(ctx, timeout)
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	return bounded, cancel, nil
}
