package representation

import (
	"context"
	"fmt"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

// WithDurability requires a synchronous control owner and fixed, durable data
// buckets. It fails construction for unsupported backends, including memory and
// the current NutsDB adapter; no ordinary Store fallback is permitted.
func WithDurability(buckets ...storage.Bucket) Option {
	return func(r *IndexRepository) {
		r.durable = true
		r.dataBuckets = append([]storage.Bucket(nil), buckets...)
	}
}

func (r *IndexRepository) Durable() bool { return r.durable }

func (r *IndexRepository) checkDurability() error {
	controls, ok := r.controls.(storage.DurableControlStore)
	if !ok {
		return storage.ErrDurabilityUnsupported
	}
	if err := controls.CheckDurability(); err != nil {
		return err
	}
	r.durableBuckets = make(map[string]storage.DurableRepresentationBucket)
	for _, bucket := range r.dataBuckets {
		data, ok := bucket.(storage.DurableRepresentationBucket)
		if !ok {
			return storage.ErrDurabilityUnsupported
		}
		if err := data.CheckDurability(); err != nil {
			return err
		}
		if _, duplicate := r.durableBuckets[data.ID()]; duplicate {
			return ErrInvalidRecord
		}
		r.durableBuckets[data.ID()] = data
	}
	if r.durableBuckets[r.initialLocation] == nil {
		return storage.ErrDurabilityUnsupported
	}
	return nil
}

func (r *IndexRepository) storeControl(ctx context.Context, md *object.Metadata) error {
	if r.durable {
		return r.controls.(storage.DurableControlStore).StoreControlSync(ctx, md.Clone())
	}
	return r.controls.StoreControl(ctx, md.Clone())
}

func (r *IndexRepository) sealChild(ctx context.Context, location string, md *object.Metadata) error {
	if !r.durable {
		return nil
	}
	bucket := r.durableBuckets[location]
	if bucket == nil {
		return storage.ErrDurabilityUnsupported
	}
	return bucket.StoreRepresentationSync(ctx, md.Clone())
}

func (r *IndexRepository) syncChildData(ctx context.Context, state *IndexState, md *object.Metadata) error {
	if !r.durable {
		return nil
	}
	bucket := r.durableBuckets[state.root.Resource.ActiveLocation]
	if bucket == nil {
		return storage.ErrDurabilityUnsupported
	}
	return bucket.SyncRepresentationData(ctx, md.Clone())
}

// Metadata surviving recovery cannot make a missing or truncated file readable.
// This check deliberately does not attach any orphan body to a root.
func (r *IndexRepository) verifyChild(ctx context.Context, location string, md *object.Metadata) error {
	if !r.durable {
		return nil
	}
	bucket := r.durableBuckets[location]
	if bucket == nil {
		return storage.ErrDurabilityUnsupported
	}
	var failure error
	md.Chunks.Range(func(index uint32) {
		if failure != nil {
			return
		}
		if failure = ctx.Err(); failure != nil {
			return
		}
		file, _, err := bucket.ReadChunkFile(ctx, md.ID, index)
		if err != nil {
			failure = err
			return
		}
		stat, err := file.Stat()
		if err == nil && stat.Size() != int64(min(md.BlockSize, md.Size-uint64(index)*md.BlockSize)) {
			err = fmt.Errorf("representation chunk %d length mismatch: %w", index, storage.ErrKeyNotFound)
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		failure = err
	})
	return failure
}
