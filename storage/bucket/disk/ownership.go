package disk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/gofrs/flock"
	"github.com/omalloc/tavern/api/defined/v1/storage"
)

// Both the data path and DB path have an owner. Using a different index path
// must not grant a second process permission to overwrite the same data files.
func acquireOwnership(paths ...string) ([]*flock.Flock, error) {
	var locks []*flock.Flock
	release := func() {
		for _, lock := range locks {
			lock.Close()
		}
	}
	normalized := make(map[string]bool)
	for _, path := range paths {
		if err := os.MkdirAll(path, 0755); err != nil {
			release()
			return nil, err
		}
		path, err := filepath.EvalSymlinks(path)
		if err != nil {
			release()
			return nil, err
		}
		path, err = filepath.Abs(path)
		if err != nil {
			release()
			return nil, err
		}
		normalized[path] = true
	}
	ordered := make([]string, 0, len(normalized))
	for path := range normalized {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		lock := flock.New(filepath.Join(path, ".tavern-writer.lock"))
		ok, err := lock.TryLock()
		if err != nil || !ok {
			lock.Close()
			release()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("storage directory already has a writer: %s", path)
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func (d *diskBucket) Quiesce(ctx context.Context) error { return d.handles.Drain(ctx) }

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

func (d *diskBucket) releaseOwnership() error {
	var failures []error
	for _, lock := range d.ownership {
		failures = append(failures, lock.Close())
	}
	return errors.Join(failures...)
}
