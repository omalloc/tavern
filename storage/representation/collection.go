package representation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

func (r *IndexRepository) verifyAt(ctx context.Context, location string, md *object.Metadata) error {
	bucket := r.buckets[location]
	if bucket == nil {
		return storage.ErrControlUnsupported
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
			err = fmt.Errorf("invalid representation chunk length: %w", storage.ErrKeyNotFound)
		}
		if err == nil && md.Representation != nil {
			if expected, exists := md.Representation.ChunkDigests[index]; exists {
				digest := sha256.New()
				if _, err = io.Copy(digest, file); err == nil && hex.EncodeToString(digest.Sum(nil)) != expected {
					err = fmt.Errorf("invalid representation chunk digest: %w", storage.ErrKeyNotFound)
				}
			}
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		failure = err
	})
	return failure
}

// collectionKey accepts only known v2 physical identities. Unknown/malformed
// records are retained, never guessed into a root or deleted by filename.
func (r *IndexRepository) collectionKey(ctx context.Context, md *object.Metadata) (object.ResourceKey, error) {
	if md == nil || !md.ID.IsRepresentation() || md.Representation == nil || md.Representation.SchemaVersion != object.RepresentationSchemaVersion {
		return object.ResourceKey{}, ErrInvalidRecord
	}
	info := md.Representation
	control, err := object.ResourceControlID(md.ID.Path(), info.ResourceID)
	if err != nil {
		return object.ResourceKey{}, err
	}
	root, err := r.controls.LoadControl(ctx, control)
	if err == nil {
		if root == nil || root.Resource == nil {
			return object.ResourceKey{}, ErrInvalidRecord
		}
		key, err := object.ResourceKeyFromRecord(root.Resource)
		if err != nil {
			return key, err
		}
		fingerprint := info.StorageSelectorFingerprint
		if fingerprint == "" {
			for _, ref := range root.Resource.Variants {
				if sameID(ref.RepresentationID, md.ID) {
					fingerprint = ref.SelectorFingerprint
					break
				}
			}
		}
		expected, err := key.RepresentationID(info.Incarnation, info.PurgeEpoch, fingerprint, info.BodyGeneration)
		if err != nil || !sameID(expected, md.ID) {
			return object.ResourceKey{}, ErrInvalidRecord
		}
		return key, nil
	}
	if !errors.Is(err, storage.ErrKeyNotFound) {
		return object.ResourceKey{}, err
	}
	key, err := object.ResourceKeyFromRecord(&object.ResourceRecord{CanonicalURL: md.ID.Path(), Namespace: "orphan", ResourceID: info.ResourceID})
	if err != nil {
		return key, err
	}
	expected, err := key.RepresentationID(info.Incarnation, info.PurgeEpoch, info.StorageSelectorFingerprint, info.BodyGeneration)
	if err != nil || !sameID(expected, md.ID) {
		return object.ResourceKey{}, ErrInvalidRecord
	}
	return key, nil
}

func (r *IndexRepository) collectCandidate(ctx context.Context, location string, md *object.Metadata) (bool, error) {
	key, err := r.collectionKey(ctx, md)
	if err != nil {
		return false, err
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	identity := dataIdentity{md.ID.Hash(), location}
	if shard.readers[identity] > 0 || shard.writers[identity] > 0 {
		return false, nil
	}
	if migration := shard.migrations[key.ID().Hash()]; migration != nil {
		return false, nil
	}
	for _, lease := range shard.pending[key.ID().Hash()] {
		if lease.selector.SelectorFingerprint == md.Representation.StorageSelectorFingerprint {
			return false, nil
		}
	}
	root, err := r.load(ctx, key)
	if err == nil {
		epochs, err := r.barriersFor(ctx, key)
		if err != nil {
			return false, err
		}
		if root.Resource.BarrierIncarnation != epochs.incarnation {
			return false, ErrInvalidRecord
		}
		if root.Resource.ActiveLocation == location && root.Resource.PrefixHardEpoch == epochs.hard {
			for _, ref := range root.Resource.Variants {
				if sameID(ref.RepresentationID, md.ID) {
					return false, nil
				}
			}
		}
		if r.durable && !r.writeback {
			// A prior synchronous write may have returned an ambiguous I/O
			// error after becoming visible. Confirm current logical state is
			// durable before deleting bytes that an older state could reference.
			catalog, err := r.loadCatalog(ctx)
			if err != nil {
				return false, err
			}
			if err := r.storeControl(ctx, catalog); err != nil {
				return false, err
			}
			if err := r.storeControl(ctx, root); err != nil {
				return false, err
			}
		}
	} else if !errors.Is(err, storage.ErrKeyNotFound) {
		return false, err
	}
	bucket := r.buckets[location]
	if bucket == nil {
		return false, storage.ErrControlUnsupported
	}
	if err := bucket.Discard(ctx, md.ID); err != nil && !errors.Is(err, storage.ErrKeyNotFound) && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// EvictReference cuts the current reference(s) under the resource lock before
// collection. Source eviction also retires products depending on its identity.
func (r *IndexRepository) EvictReference(ctx context.Context, location string, md *object.Metadata) error {
	key, err := r.collectionKey(ctx, md)
	if err != nil {
		return err
	}
	r.barrierMu.RLock()
	shard := r.shard(key)
	shard.mu.Lock()
	root, err := r.load(ctx, key)
	if err == nil && root.Resource.ActiveLocation == location {
		retained := make([]object.VariantRef, 0, len(root.Resource.Variants))
		for _, ref := range root.Resource.Variants {
			if sameID(ref.RepresentationID, md.ID) || ref.SelectionKind == object.SelectionManaged && sameID(ref.SelectedSourceID, md.ID) {
				continue
			}
			retained = append(retained, ref.Clone())
		}
		if len(retained) != len(root.Resource.Variants) {
			if root.Resource.Revision == ^uint64(0) {
				err = ErrInvalidRecord
			} else {
				root.Resource.Variants = retained
				root.Resource.Revision++
				err = r.storeControl(ctx, root)
			}
		}
	}
	shard.mu.Unlock()
	r.barrierMu.RUnlock()
	if err != nil && !errors.Is(err, storage.ErrKeyNotFound) {
		return err
	}
	_, err = r.collectCandidate(ctx, location, md)
	return err
}
