package representation

import (
	"context"
	"math"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

// RetiredRepresentation locates an unreferenced body at the location captured
// when its reference was retired. A later resource migration must not redirect
// cleanup to a different bucket.
type RetiredRepresentation struct {
	ID       *object.ID `json:"id" yaml:"id"`
	Location string     `json:"location" yaml:"location"`
}

// Retire removes exactly the observed reference, including its revision, before
// returning the stored physical ID for eventual cleanup. It does not delete
// bytes or act as PURGE: P2 adds reader leases and invalidation coordination.
// An old observation cannot retire a newer 304 update of the same body ID.
func (r *IndexRepository) Retire(ctx context.Context, key object.ResourceKey, expected object.VariantRef) (*RetiredRepresentation, error) {
	if !key.Valid() || validateReference(expected) != nil {
		return nil, ErrInvalidRecord
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	root, err := r.load(ctx, key)
	if err != nil {
		return nil, err
	}
	if root.Resource.Revision == math.MaxUint64 {
		return nil, ErrInvalidRecord
	}
	var retained []object.VariantRef
	var retired *object.ID
	for _, ref := range root.Resource.Variants {
		if sameReference(ref, expected) {
			retired = ref.RepresentationID.Clone()
			continue
		}
		if expected.SelectionKind == object.SelectionHTTP && ref.SelectionKind == object.SelectionManaged && sameID(ref.SelectedSourceID, expected.RepresentationID) {
			continue
		}
		retained = append(retained, ref.Clone())
	}
	if retired == nil {
		return nil, ErrSuperseded
	}
	// A physical body can only be collected after its final reference goes.
	for _, ref := range retained {
		if sameID(ref.RepresentationID, retired) {
			return nil, ErrInvalidRecord
		}
	}
	root.Resource.Variants = retained
	root.Resource.Revision++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.storeControl(ctx, root); err != nil {
		return nil, err
	}
	return &RetiredRepresentation{ID: retired, Location: root.Resource.ActiveLocation}, nil
}
