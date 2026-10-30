package diraware

import (
	"context"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/representation"
)

// Checker decides whether a cached object should be marked expired.
// Return true to mark expired.
type Checker interface {
	Marked(ctx context.Context, id *object.ID, md *object.Metadata) (bool, error)
	TrieAdd(ctx context.Context, storePath string)
}

// New wraps a storage with push-mark logic.
// If checker is nil, returns the original storage.
func New(base storagev1.Storage, checker Checker) storagev1.Storage {
	if base == nil || checker == nil {
		return base
	}
	return &wrappedStorage{
		base:    base,
		checker: checker,
	}
}

type wrappedStorage struct {
	base    storagev1.Storage
	checker Checker
}

func (w *wrappedStorage) Select(ctx context.Context, id *object.ID) storagev1.Bucket {
	return wrapBucket(w.base.Select(ctx, id), w.checker)
}

func (w *wrappedStorage) Rebuild(ctx context.Context, buckets []storagev1.Bucket) error {
	return w.base.Rebuild(ctx, buckets)
}

func (w *wrappedStorage) Buckets() []storagev1.Bucket {
	baseBuckets := w.base.Buckets()
	wrapped := make([]storagev1.Bucket, 0, len(baseBuckets))
	for _, b := range baseBuckets {
		wrapped = append(wrapped, wrapBucket(b, w.checker))
	}
	return wrapped
}

func (w *wrappedStorage) RepresentationBuckets() []storagev1.Bucket {
	if provider, ok := w.base.(storagev1.RepresentationBucketProvider); ok {
		return provider.RepresentationBuckets()
	}
	return w.base.Buckets()
}

func (w *wrappedStorage) SharedKV() storagev1.SharedKV {
	return w.base.SharedKV()
}

func (w *wrappedStorage) PURGE(storeUrl string, typ storagev1.PurgeControl) error {
	return w.base.PURGE(storeUrl, typ)
}

func (w *wrappedStorage) RegisterRepresentationPurger(name string, purger storagev1.RepresentationPurger) error {
	registry, ok := w.base.(storagev1.RepresentationRegistry)
	if !ok {
		return storagev1.ErrControlUnsupported
	}
	return registry.RegisterRepresentationPurger(name, purger)
}

func (w *wrappedStorage) BeginLegacyFill(ctx context.Context, raw string) (storagev1.LegacyFillLease, error) {
	coordinator, ok := w.base.(storagev1.LegacyFillCoordinator)
	if !ok {
		return nil, storagev1.ErrControlUnsupported
	}
	return coordinator.BeginLegacyFill(ctx, raw)
}

func (w *wrappedStorage) Close() error {
	return w.base.Close()
}

func (w *wrappedStorage) BeginCacheTask(ctx context.Context, timeout time.Duration) (context.Context, func(), error) {
	if tasks, ok := w.base.(storagev1.TaskCoordinator); ok {
		return tasks.BeginCacheTask(ctx, timeout)
	}
	return ctx, func() {}, nil
}
func (w *wrappedStorage) Drain(ctx context.Context) error {
	if tasks, ok := w.base.(storagev1.TaskCoordinator); ok {
		return tasks.Drain(ctx)
	}
	return nil
}

func (w *wrappedStorage) RepresentationManager() *representation.Manager {
	if provider, ok := w.base.(representation.Provider); ok {
		return provider.RepresentationManager()
	}
	return nil
}
