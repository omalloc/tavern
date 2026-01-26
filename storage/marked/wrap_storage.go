package marked

import (
	"context"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

// Checker decides whether a cached object should be marked expired.
// Return true to mark expired.
type Checker interface {
	Marked(ctx context.Context, id *object.ID, md *object.Metadata) (bool, error)
	TrieAdd(ctx context.Context, storePath string)
}

// WrapStorage wraps a storage with push-mark logic.
// If checker is nil, returns the original storage.
func WrapStorage(base storagev1.Storage, checker Checker) storagev1.Storage {
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
	return w.base.Buckets()
}

func (w *wrappedStorage) SharedKV() storagev1.SharedKV {
	return w.base.SharedKV()
}

func (w *wrappedStorage) PURGE(storeUrl string, typ storagev1.PurgeControl) error {
	// 添加推送目录到前缀树
	// TODO:
	//	1. sharedkv 保存任务
	//	2. 重新启动要还原回来
	//  3. 任务多久过期
	//  4. 凌晨2-4点低峰时期进行 Hard Delete, 然后删除任务
	if typ.Dir && typ.MarkExpired {
		w.checker.TrieAdd(context.Background(), storeUrl)
		return nil
	}
	return w.base.PURGE(storeUrl, typ)
}

func (w *wrappedStorage) SelectWithType(ctx context.Context, id *object.ID, tier string) storagev1.Bucket {
	return wrapBucket(w.base.SelectWithType(ctx, id, tier), w.checker)
}

func (w *wrappedStorage) Promote(ctx context.Context, id *object.ID, src storagev1.Bucket) error {
	return w.base.Promote(ctx, id, src)
}

func (w *wrappedStorage) Close() error {
	return w.base.Close()
}
