package representation

import (
	"context"
	"errors"
	"math"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

// IndexState is a fill ticket captured before an origin request, including the
// resource epochs, prefix barrier epochs and fixed storage location.
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
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if r.migratingLocked(key) {
		return nil, ErrMigrationBusy
	}
	root, changed, err := r.rootForFill(ctx, key)
	if err != nil {
		return nil, err
	}
	if changed {
		if root.Resource.Revision == math.MaxUint64 {
			return nil, ErrInvalidRecord
		}
		root.Resource.Revision++
		if err := r.storeControl(ctx, root); err != nil {
			return nil, err
		}
	}
	return &IndexState{key: key, root: root}, nil
}

func currentState(state *IndexState, root *object.Metadata) bool {
	if state == nil || state.root == nil || root == nil || root.Resource == nil {
		return false
	}
	a, b := state.root.Resource, root.Resource
	return a.ResourceID == b.ResourceID && a.Incarnation == b.Incarnation && a.PurgeEpoch == b.PurgeEpoch &&
		a.RevalidateEpoch == b.RevalidateEpoch && a.RoutingEpoch == b.RoutingEpoch && a.ActiveLocation == b.ActiveLocation &&
		a.PrefixHardEpoch == b.PrefixHardEpoch && a.PrefixSoftEpoch == b.PrefixSoftEpoch && a.BarrierIncarnation == b.BarrierIncarnation
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
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(state.key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if r.migratingLocked(state.key) {
		return nil, ErrMigrationBusy
	}
	current, err := r.currentBarriers(ctx, state)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, ErrSuperseded
	}
	root, err := r.load(ctx, state.key)
	if err != nil {
		return nil, err
	}
	if !currentState(state, root) {
		return nil, ErrSuperseded
	}
	if selector.SelectionKind == object.SelectionManaged {
		if _, err := r.currentSource(ctx, state.key, root.Resource, selector); err != nil {
			return nil, err
		}
	}
	return r.reserveLocked(ctx, state.key, root.Resource, selector, limit)
}

// Publish merges references into the latest root and consumes the reservation.
// storeChild runs under the short resource lock, after conflict checks. It is
// local metadata I/O only; it must not read a body or perform network I/O.
// storeChild publishes final metadata for both 200 and 304 without allowing a late validator to overwrite
// another validation of the same ID. In durable mode the child files and metadata
// are sealed before the synchronous root update, which is the publication point.
func (r *IndexRepository) Publish(ctx context.Context, state *IndexState, lease *Reservation, ref object.VariantRef, md *object.Metadata, previous *object.VariantRef, storeChild func() error) (bool, error) {
	if lease == nil {
		return false, ErrInvalidRecord
	}
	defer r.Release(lease)
	if state == nil || !state.key.Valid() || lease.key.ResourceID() != state.key.ResourceID() || !sameSelector(lease.selector, ref) || validateReference(ref) != nil {
		return false, ErrInvalidRecord
	}
	if md == nil || !md.HasReadableChunks() {
		return false, ErrInvalidRecord
	}
	if err := r.syncChildData(ctx, state, md); err != nil {
		return false, err
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(state.key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.migratingLocked(state.key) {
		return false, nil
	}
	current, err := r.currentBarriers(ctx, state)
	if err != nil {
		return false, err
	}
	if !current {
		return false, nil
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
	if r.lifecycle && !sameID(lease.boundID, ref.RepresentationID) && (previous == nil || !sameID(previous.RepresentationID, ref.RepresentationID)) {
		return false, ErrInvalidRecord
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
			(existing.ResponseDate == ref.ResponseDate && (previous == nil || !sameReference(existing, *previous)) &&
				!sameID(existing.RepresentationID, ref.RepresentationID) && existing.RepresentationID.HashStr() < ref.RepresentationID.HashStr())) {
			return false, nil
		}
	}
	if !previousFound {
		return false, nil
	}
	if ref.SelectionKind == object.SelectionManaged {
		source, err := r.currentSource(ctx, state.key, root.Resource, ref)
		if err != nil {
			if errors.Is(err, ErrSuperseded) {
				return false, nil
			}
			return false, err
		}
		if md.ExpiresAt > source.ExpiresAt || md.Representation.ValidatedEpoch != source.Representation.ValidatedEpoch {
			return false, ErrInvalidRecord
		}
		if _, allowed := xhttp.ParseCacheTime(protocol.ProtocolCacheTime, md.Headers); !allowed {
			return false, ErrInvalidRecord
		}
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
	if err := r.sealChild(ctx, root.Resource.ActiveLocation, child); err != nil {
		return false, err
	}
	var merged []object.VariantRef
	for _, existing := range root.Resource.Variants {
		if sameSelector(existing, ref) || previous != nil && sameReference(existing, *previous) {
			continue
		}
		dependent := false
		if existing.SelectionKind == object.SelectionManaged {
			for _, source := range root.Resource.Variants {
				if sameID(existing.SelectedSourceID, source.RepresentationID) && originReplaced(source, ref, previous) {
					dependent = true
					break
				}
			}
		}
		if dependent {
			continue
		}
		merged = append(merged, existing.Clone())
	}
	root.Resource.Revision++
	root.Resource.Variants = append(merged, ref.Clone())
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := r.storeControl(ctx, root); err != nil {
		return false, err
	}
	return true, nil
}
