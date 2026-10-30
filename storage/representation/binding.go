package representation

import (
	"context"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

// BindFill claims a fresh physical body before any bytes are written. A logical
// selector can have multiple reservations, but two writers cannot own one ID.
func (r *IndexRepository) BindFill(ctx context.Context, state *IndexState, lease *Reservation, md *object.Metadata) error {
	if state == nil || lease == nil || md == nil || md.Representation == nil || !md.ID.IsRepresentation() || !state.key.Valid() || lease.key.ResourceID() != state.key.ResourceID() {
		return ErrInvalidRecord
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(state.key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if r.migratingLocked(state.key) {
		return ErrMigrationBusy
	}
	current, err := r.currentBarriers(ctx, state)
	if err != nil {
		return err
	}
	if !current {
		return ErrSuperseded
	}
	root, err := r.load(ctx, state.key)
	if err != nil {
		return err
	}
	if lease.released || !currentState(state, root) || shard.pending[state.key.ID().Hash()][lease.id] != lease {
		return ErrSuperseded
	}
	info := md.Representation
	if info.SchemaVersion != object.RepresentationSchemaVersion || info.StorageSelectorFingerprint != lease.selector.SelectorFingerprint || lease.boundID != nil && !sameID(lease.boundID, md.ID) {
		return ErrInvalidRecord
	}
	expected, err := state.key.RepresentationID(root.Resource.Incarnation, root.Resource.PurgeEpoch, lease.selector.SelectorFingerprint, info.BodyGeneration)
	if err != nil || !sameID(expected, md.ID) || info.ResourceID != root.Resource.ResourceID || info.Incarnation != root.Resource.Incarnation || info.PurgeEpoch != root.Resource.PurgeEpoch {
		return ErrInvalidRecord
	}
	for _, ref := range root.Resource.Variants {
		if sameID(ref.RepresentationID, md.ID) {
			return ErrSuperseded
		}
	}
	for _, other := range shard.pending[state.key.ID().Hash()] {
		if other != lease && sameID(other.boundID, md.ID) {
			return ErrSuperseded
		}
	}
	identity := dataIdentity{md.ID.Hash(), root.Resource.ActiveLocation}
	if lease.boundID == nil {
		if shard.writers[identity] > 0 {
			return ErrSuperseded
		}
		if shard.writers == nil {
			shard.writers = make(map[dataIdentity]int)
		}
		shard.writers[identity]++
		lease.boundLocation = root.Resource.ActiveLocation
	}
	lease.boundID = md.ID.Clone()
	return nil
}

// JournalFill persists cleanup ownership before a writer creates a chunk. A
// durable journal accepts incomplete descriptors but never publishes a root.
func (r *IndexRepository) JournalFill(ctx context.Context, state *IndexState, lease *Reservation, bucket storage.Bucket, md *object.Metadata) error {
	if state == nil || lease == nil || bucket == nil || md == nil || !state.key.Valid() {
		return ErrInvalidRecord
	}
	shard := r.shard(state.key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if lease.released || !sameID(lease.boundID, md.ID) || bucket.ID() != lease.boundLocation {
		return ErrSuperseded
	}
	if r.durable {
		data := r.durableBuckets[bucket.ID()]
		if data == nil {
			return storage.ErrDurabilityUnsupported
		}
		return data.StoreRepresentationJournalSync(ctx, md)
	}
	return bucket.Store(ctx, md)
}
