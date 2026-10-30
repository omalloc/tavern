package storage

import (
	"context"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/pkg/lifecycle"
)

var _ storagev1.TaskCoordinator = (*nativeStorage)(nil)
var _ storagev1.TaskCoordinator = (*migratorStorage)(nil)

func (n *nativeStorage) BeginCacheTask(ctx context.Context, timeout time.Duration) (context.Context, func(), error) {
	return n.tasks.Begin(ctx, timeout)
}
func (m *migratorStorage) BeginCacheTask(ctx context.Context, timeout time.Duration) (context.Context, func(), error) {
	return m.tasks.Begin(ctx, timeout)
}
func drain(ctx context.Context, tasks *lifecycle.Group, buckets []storagev1.Bucket) error {
	if err := tasks.Drain(ctx); err != nil {
		return err
	}
	for _, bucket := range buckets {
		if q, ok := bucket.(storagev1.Quiescer); ok {
			if err := q.Quiesce(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
func (n *nativeStorage) Drain(ctx context.Context) error {
	buckets := n.Buckets()
	if n.memoryBucket != nil {
		buckets = append(buckets, n.memoryBucket)
	}
	return drain(ctx, &n.tasks, buckets)
}
func (m *migratorStorage) Drain(ctx context.Context) error {
	buckets := m.Buckets()
	if m.memoryBucket != nil {
		buckets = append(buckets, m.memoryBucket)
	}
	return drain(ctx, &m.tasks, buckets)
}
