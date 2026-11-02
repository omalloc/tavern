package representation

import (
	"context"
	"errors"
	"io"
	"math"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var ErrMigrationBusy = errors.New("representation resource is migrating")

type migrationState struct {
	root   *object.Metadata
	target string
}

func (r *IndexRepository) migratingLocked(key object.ResourceKey) bool {
	return r.shard(key).migrations[key.ID().Hash()] != nil
}

// MigrateResource copies one entire resource set. Control ownership never
// moves. Data is copied and synced outside resource locks; cutover requires the
// original revision, epochs, prefix barriers and routing identity still match.
func (r *IndexRepository) MigrateResource(ctx context.Context, key object.ResourceKey, target string) error {
	if !key.Valid() || r.buckets[target] == nil {
		return ErrInvalidRecord
	}
	r.barrierMu.RLock()
	shard := r.shard(key)
	shard.mu.Lock()
	if r.migratingLocked(key) {
		shard.mu.Unlock()
		r.barrierMu.RUnlock()
		return ErrMigrationBusy
	}
	root, changed, err := r.rootForFill(ctx, key)
	if err != nil {
		shard.mu.Unlock()
		r.barrierMu.RUnlock()
		return err
	}
	if changed {
		if root.Resource.Revision == math.MaxUint64 {
			shard.mu.Unlock()
			r.barrierMu.RUnlock()
			return ErrInvalidRecord
		}
		root.Resource.Revision++
		if err := r.storeControl(ctx, root); err != nil {
			shard.mu.Unlock()
			r.barrierMu.RUnlock()
			return err
		}
	}
	if root.Resource.ActiveLocation == target {
		shard.mu.Unlock()
		r.barrierMu.RUnlock()
		return nil
	}
	source := r.buckets[root.Resource.ActiveLocation]
	if source == nil {
		shard.mu.Unlock()
		r.barrierMu.RUnlock()
		return storage.ErrControlUnsupported
	}
	ids := make([]*object.ID, 0, len(root.Resource.Variants))
	for _, ref := range root.Resource.Variants {
		if validateReference(ref) != nil {
			shard.mu.Unlock()
			r.barrierMu.RUnlock()
			return ErrInvalidRecord
		}
		if shard.readers[dataIdentity{ref.RepresentationID.Hash(), target}] > 0 {
			shard.mu.Unlock()
			r.barrierMu.RUnlock()
			return ErrMigrationBusy
		}
		ids = append(ids, ref.RepresentationID)
	}
	lease := r.pinLocked(key, root.Resource.ActiveLocation, ids...)
	if shard.migrations == nil {
		shard.migrations = make(map[object.IDHash]*migrationState)
	}
	marker := &migrationState{root: root.Clone(), target: target}
	shard.migrations[key.ID().Hash()] = marker
	shard.mu.Unlock()
	r.barrierMu.RUnlock()
	defer lease.Close()
	defer func() {
		shard.mu.Lock()
		if shard.migrations[key.ID().Hash()] == marker {
			delete(shard.migrations, key.ID().Hash())
		}
		shard.mu.Unlock()
	}()
	for _, ref := range root.Resource.Variants {
		md, err := r.lookup(ctx, root.Resource.ActiveLocation, ref.RepresentationID)
		if err != nil {
			return err
		}
		if !validChild(key, root.Resource, ref, md) {
			return ErrInvalidRecord
		}
		journal := func(md *object.Metadata) error { return r.buckets[target].Store(ctx, md) }
		if r.durable {
			journal = func(md *object.Metadata) error {
				return r.durableBuckets[target].StoreRepresentationJournalSync(ctx, md)
			}
		}
		if err := copyRepresentation(ctx, source, r.buckets[target], md, journal); err != nil {
			return err
		}
		if r.durable {
			data := r.durableBuckets[target]
			if data == nil {
				return storage.ErrDurabilityUnsupported
			}
			if err := data.SyncRepresentationData(ctx, md); err != nil {
				return err
			}
			if err := data.StoreRepresentationSync(ctx, md); err != nil {
				return err
			}
		}
		if err := r.verifyAt(ctx, target, md); err != nil {
			return err
		}
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard.mu.Lock()
	defer shard.mu.Unlock()
	current, err := r.load(ctx, key)
	if err != nil {
		return err
	}
	epochs, err := r.barriersFor(ctx, key)
	if err != nil {
		return err
	}
	if current.Resource.Revision != root.Resource.Revision || !currentState(&IndexState{key: key, root: root}, current) ||
		epochs.incarnation != root.Resource.BarrierIncarnation || epochs.hard != root.Resource.PrefixHardEpoch || epochs.soft != root.Resource.PrefixSoftEpoch {
		return ErrSuperseded
	}
	if current.Resource.Revision == math.MaxUint64 || current.Resource.RoutingEpoch == math.MaxUint64 {
		return ErrInvalidRecord
	}
	current.Resource.Revision++
	current.Resource.RoutingEpoch++
	current.Resource.ActiveLocation = target
	if err := r.storeControlSync(ctx, current); err != nil {
		return err
	}
	return nil
}

func copyRepresentation(ctx context.Context, source, target storage.Bucket, md *object.Metadata, journal func(*object.Metadata) error) error {
	copy := md.Clone()
	copy.Chunks.Clear()
	copy.Parts.Clear()
	var failure error
	md.Chunks.Range(func(index uint32) {
		if failure != nil {
			return
		}
		if failure = ctx.Err(); failure != nil {
			return
		}
		copy.Parts.Set(index)
		if failure = journal(copy); failure != nil {
			return
		}
		reader, _, err := source.ReadChunkFile(ctx, md.ID, index)
		if err != nil {
			failure = err
			return
		}
		writer, _, err := target.WriteChunkFile(ctx, md.ID, index)
		if err != nil {
			reader.Close()
			failure = err
			return
		}
		n, err := io.Copy(writer, reader)
		readClose := reader.Close()
		writeClose := writer.Close()
		if err == nil {
			err = readClose
		}
		if err == nil {
			err = writeClose
		}
		if err == nil && n != int64(min(md.BlockSize, md.Size-uint64(index)*md.BlockSize)) {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			failure = err
			return
		}
		copy.Parts.Remove(index)
		copy.Chunks.Set(index)
		failure = target.Store(ctx, copy)
	})
	return failure
}
