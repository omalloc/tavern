package storage

import (
	"context"
	"errors"
	"fmt"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/omalloc/tavern/storage/bucket/legacygc"
)

func (r *representationRegistry) collectLegacyVary(ctx context.Context, buckets []storagev1.Bucket, records, files int) (result storagev1.LegacyCollectionResult, failure error) {
	defer func() {
		pkgmetrics.RecordLegacyCollection(result.Collected, result.Skipped, result.FileOps, failure != nil)
	}()
	if records <= 0 || records > 128 || files < 2 || files > 1024 || len(buckets) == 0 {
		return result, storagev1.ErrInvalidationBudget
	}
	for _, bucket := range buckets {
		if _, ok := bucket.(storagev1.MetadataScanner); !ok {
			return result, storagev1.ErrControlUnsupported
		}
		if _, ok := bucket.(storagev1.LegacyVaryReclaimer); !ok {
			return result, storagev1.ErrControlUnsupported
		}
	}
	r.gcMu.Lock()
	defer r.gcMu.Unlock()
	if r.legacyCursors == nil {
		r.legacyCursors = make(map[string][]byte)
	}
	for visited := 0; visited < len(buckets) && result.Scanned < records && result.FileOps+2 <= files; visited++ {
		r.legacyNext %= len(buckets)
		bucket := buckets[r.legacyNext]
		batch, next, err := bucket.(storagev1.MetadataScanner).ScanMetadata(ctx, r.legacyCursors[bucket.ID()], records-result.Scanned)
		if err != nil {
			return result, err
		}
		// Advance only processed records. A large object's partial journal is
		// revisited immediately, while active-reader skips do not block other URLs.
		for _, md := range batch {
			if result.FileOps+2 > files {
				return result, nil
			}
			result.Scanned++
			if md == nil || md.ID == nil {
				return result, fmt.Errorf("legacy scan metadata has no physical identity")
			}
			if !legacygc.Eligible(md) {
				result.Skipped++
				r.legacyCursors[bucket.ID()] = append([]byte(nil), md.ID.Bytes()...)
				continue
			}
			count, done, err := r.collectLegacyCandidate(ctx, buckets, bucket, md, files-result.FileOps)
			result.FileOps += count
			if err != nil {
				return result, err
			}
			if done {
				result.Collected++
			} else {
				result.Skipped++
				if count > 0 {
					return result, nil
				}
			}
			r.legacyCursors[bucket.ID()] = append([]byte(nil), md.ID.Bytes()...)
		}
		r.legacyCursors[bucket.ID()] = next
		if next == nil {
			r.legacyNext = (r.legacyNext + 1) % len(buckets)
		}
	}
	return result, nil
}
func (r *representationRegistry) collectLegacyCandidate(ctx context.Context, buckets []storagev1.Bucket, bucket storagev1.Bucket, md *object.Metadata, budget int) (int, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	canonical, err := canonicalPurge(md.ID.Path(), false)
	if err != nil {
		return 0, false, nil
	}
	for fill := range r.fills {
		if fill.url == canonical && !fill.closed {
			return 0, false, nil
		}
	}
	// Revoke actual URL roots everywhere before deleting an individual physical
	// ID. Old VirtualKey strings are never interpreted as deletion instructions.
	root := object.NewID(md.ID.Path())
	for _, owner := range buckets {
		if err = owner.(storagev1.LegacyVaryReclaimer).RetireLegacyVaryRoot(ctx, root); err != nil {
			return 0, false, fmt.Errorf("retire legacy root: %w", err)
		}
	}
	processed, done, err := bucket.(storagev1.LegacyVaryReclaimer).ReclaimLegacyVary(ctx, md.ID, budget)
	if errors.Is(err, legacygc.ErrNotRetired) {
		return processed, false, nil
	}
	return processed, done, err
}
func (n *nativeStorage) CollectLegacyVary(ctx context.Context, records, files int) (storagev1.LegacyCollectionResult, error) {
	ctx, finish, err := n.tasks.Begin(ctx, 0)
	if err != nil {
		return storagev1.LegacyCollectionResult{}, err
	}
	defer finish()
	return n.representations.collectLegacyVary(ctx, n.RepresentationBuckets(), records, files)
}
func (m *migratorStorage) CollectLegacyVary(ctx context.Context, records, files int) (storagev1.LegacyCollectionResult, error) {
	ctx, finish, err := m.tasks.Begin(ctx, 0)
	if err != nil {
		return storagev1.LegacyCollectionResult{}, err
	}
	defer finish()
	return m.representations.collectLegacyVary(ctx, m.RepresentationBuckets(), records, files)
}

var _ storagev1.LegacyVaryCollector = (*nativeStorage)(nil)
var _ storagev1.LegacyVaryCollector = (*migratorStorage)(nil)
