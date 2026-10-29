package representation

import (
	"context"
	"errors"
	"math"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

// IndexState captures resource identity before an origin request. P2 extends
// this boundary with persistent prefix barriers and storage ownership checks.
type IndexState struct {
	key  object.ResourceKey
	root *object.Metadata
}

func (s *IndexState) Resource() *object.Metadata { return s.root.Clone() }
func (s *IndexState) Key() object.ResourceKey    { return s.key }

func (r *IndexRepository) Capture(ctx context.Context, key object.ResourceKey) (*IndexState, error) {
	if !key.Valid() {
		return nil, ErrInvalidRecord
	}
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	root, err := r.load(ctx, key)
	if errors.Is(err, storage.ErrKeyNotFound) {
		root, err = r.updateLocked(ctx, key, nil)
	}
	if err != nil {
		return nil, err
	}
	return &IndexState{key: key, root: root}, nil
}

func currentState(state *IndexState, root *object.Metadata) bool {
	if state == nil || state.root == nil || root == nil || root.Resource == nil {
		return false
	}
	a, b := state.root.Resource, root.Resource
	return a.ResourceID == b.ResourceID && a.Incarnation == b.Incarnation && a.PurgeEpoch == b.PurgeEpoch &&
		a.RevalidateEpoch == b.RevalidateEpoch && a.RoutingEpoch == b.RoutingEpoch && a.ActiveLocation == b.ActiveLocation
}

func sameReference(a, b object.VariantRef) bool {
	return sameID(a.RepresentationID, b.RepresentationID) && a.Revision == b.Revision && a.ResponseDate == b.ResponseDate && sameSelector(a, b)
}

func (r *IndexRepository) ReserveFor(ctx context.Context, state *IndexState, selector object.VariantRef, limit int) (*Reservation, error) {
	if state == nil || !state.key.Valid() || validateSelector(selector) != nil {
		return nil, ErrInvalidRecord
	}
	if limit <= 0 {
		return nil, ErrSelectorLimit
	}
	shard := r.shard(state.key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	root, err := r.load(ctx, state.key)
	if err != nil {
		return nil, err
	}
	if !currentState(state, root) {
		return nil, ErrSuperseded
	}
	return r.reserveLocked(ctx, state.key, root.Resource, selector, limit)
}

// Publish merges references into the latest root and consumes the reservation.
// storeChild runs under the short resource lock, after conflict checks. It is
// local metadata I/O only; it must not read a body or perform network I/O.
// storeChild publishes final metadata for both 200 and 304 without allowing a late validator to overwrite
// another validation of the same ID. Whole-record atomicity/durability is P2.
func (r *IndexRepository) Publish(ctx context.Context, state *IndexState, lease *Reservation, ref object.VariantRef, md *object.Metadata, previous *object.VariantRef, storeChild func() error) (bool, error) {
	if lease == nil {
		return false, ErrInvalidRecord
	}
	defer r.Release(lease)
	if state == nil || !state.key.Valid() || lease.key.ResourceID() != state.key.ResourceID() || !sameSelector(lease.selector, ref) || validateReference(ref) != nil {
		return false, ErrInvalidRecord
	}
	shard := r.shard(state.key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	root, err := r.load(ctx, state.key)
	if err != nil {
		return false, err
	}
	if !currentState(state, root) || lease.incarnation != root.Resource.Incarnation || lease.purgeEpoch != root.Resource.PurgeEpoch ||
		lease.revalidateEpoch != root.Resource.RevalidateEpoch || lease.routingEpoch != root.Resource.RoutingEpoch {
		return false, nil
	}
	if shard.pending[state.key.ID().Hash()][lease.id] != lease {
		return false, nil
	}
	candidate := ref.Clone()
	if md != nil && md.Representation != nil {
		candidate.Revision = md.Representation.Revision
	}
	if !validChild(state.key, root.Resource, candidate, md) || root.Resource.Revision == math.MaxUint64 || storeChild == nil {
		return false, ErrInvalidRecord
	}
	previousFound := previous == nil
	for _, existing := range root.Resource.Variants {
		if previous != nil && sameReference(existing, *previous) {
			previousFound = true
		}
		if sameSelector(existing, ref) && (existing.ResponseDate > ref.ResponseDate ||
			(existing.ResponseDate == ref.ResponseDate && !sameID(existing.RepresentationID, ref.RepresentationID) && existing.RepresentationID.HashStr() < ref.RepresentationID.HashStr())) {
			return false, nil
		}
	}
	if !previousFound {
		return false, nil
	}
	// A failed root write must not refresh an old reference, even when its
	// Date is unchanged. Readers require matching child/reference versions.
	ref.Revision = root.Resource.Revision + 1
	md.Representation.Revision = ref.Revision
	if err := storeChild(); err != nil {
		return false, err
	}
	child, err := r.lookup(ctx, root.Resource.ActiveLocation, ref.RepresentationID)
	if err != nil {
		return false, err
	}
	if !validChild(state.key, root.Resource, ref, child) {
		return false, ErrInvalidRecord
	}
	var merged []object.VariantRef
	for _, existing := range root.Resource.Variants {
		if sameSelector(existing, ref) || previous != nil && sameReference(existing, *previous) {
			continue
		}
		merged = append(merged, existing.Clone())
	}
	root.Resource.Revision++
	root.Resource.Variants = append(merged, ref.Clone())
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := r.controls.StoreControl(ctx, root); err != nil {
		return false, err
	}
	return true, nil
}
