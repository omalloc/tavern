package representation

import (
	"context"
	"math"

	"github.com/google/uuid"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

// Reservation is an opaque, process-local selector budget lease. It is not a
// fill ticket: IndexState captures invalidation state before an origin request.
type Reservation struct {
	id              string
	key             object.ResourceKey
	selector        object.VariantRef
	incarnation     string
	purgeEpoch      uint64
	revalidateEpoch uint64
	routingEpoch    uint64
}

// Reserve counts distinct ready and pending selectors together. Multiple fills
// of one selector each get a releasable lease but consume only one choice.
func (r *IndexRepository) Reserve(ctx context.Context, key object.ResourceKey, selector object.VariantRef, limit int) (*Reservation, error) {
	if !key.Valid() || validateSelector(selector) != nil {
		return nil, ErrInvalidRecord
	}
	if limit <= 0 {
		return nil, ErrSelectorLimit
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
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
	return r.reserveLocked(ctx, key, root.Resource, selector, limit)
}

func (r *IndexRepository) reserveLocked(ctx context.Context, key object.ResourceKey, record *object.ResourceRecord, selector object.VariantRef, limit int) (*Reservation, error) {
	shard := r.shard(key)
	if shard.pending == nil {
		shard.pending = make(map[object.IDHash]map[string]*Reservation)
	}
	pending := shard.pending[key.ID().Hash()]
	var distinct []object.VariantRef
	add := func(candidate object.VariantRef) {
		for _, existing := range distinct {
			if sameSelector(existing, candidate) {
				return
			}
		}
		distinct = append(distinct, candidate)
	}
	for _, ref := range record.Variants {
		if ref.SelectionKind == selector.SelectionKind && validateSelector(ref) == nil {
			add(ref)
		}
	}
	for id, reservation := range pending {
		if reservation.incarnation != record.Incarnation || reservation.purgeEpoch != record.PurgeEpoch ||
			reservation.revalidateEpoch != record.RevalidateEpoch || reservation.routingEpoch != record.RoutingEpoch {
			delete(pending, id)
			continue
		}
		if reservation.selector.SelectionKind == object.SelectionManaged {
			found := false
			for _, source := range record.Variants {
				if source.SelectionKind == object.SelectionHTTP && sameID(source.RepresentationID, reservation.selector.SelectedSourceID) {
					found = true
					break
				}
			}
			if !found {
				delete(pending, id)
				continue
			}
		}
		if reservation.selector.SelectionKind == selector.SelectionKind {
			add(reservation.selector)
		}
	}
	known := false
	for _, existing := range distinct {
		if sameSelector(existing, selector) {
			known = true
			break
		}
	}
	if !known && len(distinct) >= limit {
		return nil, ErrSelectorLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reservation := &Reservation{id: uuid.NewString(), key: key, selector: selector.Clone(),
		incarnation: record.Incarnation, purgeEpoch: record.PurgeEpoch,
		revalidateEpoch: record.RevalidateEpoch, routingEpoch: record.RoutingEpoch}
	if pending == nil {
		pending = make(map[string]*Reservation)
	}
	pending[reservation.id] = reservation
	shard.pending[key.ID().Hash()] = pending
	return reservation, nil
}

// Release is idempotent, also for a lease whose root has since been invalidated.
func (r *IndexRepository) Release(reservation *Reservation) {
	if reservation == nil || !reservation.key.Valid() {
		return
	}
	shard := r.shard(reservation.key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	key := reservation.key.ID().Hash()
	pending := shard.pending[key]
	delete(pending, reservation.id)
	if len(pending) == 0 {
		delete(shard.pending, key)
	}
}
