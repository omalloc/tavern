package representation

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

func managedSelector(source *object.ID, version, transform string) (object.VariantRef, error) {
	fingerprint, err := object.ManagedSelectorFingerprint(source, version, transform)
	if err != nil {
		return object.VariantRef{}, ErrInvalidRecord
	}
	return object.VariantRef{SelectionKind: object.SelectionManaged, SelectorFingerprint: fingerprint, TransformFingerprint: transform,
		SelectedSourceID: source.Clone(), SelectedSourceVersion: version}, nil
}

func NewManagedSelector(source *LookupResult, transform string) (object.VariantRef, error) {
	if source == nil || !source.Fresh || source.Representation == nil || source.Representation.RecordKind != object.RecordRepresentation || source.Representation.Representation == nil || !source.Representation.HasComplete() ||
		source.Representation.Representation.Kind != object.RepresentationOrigin || !sameID(source.Reference.RepresentationID, source.Representation.ID) {
		return object.VariantRef{}, ErrInvalidRecord
	}
	return managedSelector(source.Representation.ID, source.Representation.Representation.BodyGeneration, transform)
}

func (r *IndexRepository) currentSource(ctx context.Context, key object.ResourceKey, root *object.ResourceRecord, ref object.VariantRef) (*object.Metadata, error) {
	for _, candidate := range root.Variants {
		if candidate.SelectionKind != object.SelectionHTTP || !sameID(candidate.RepresentationID, ref.SelectedSourceID) {
			continue
		}
		md, err := r.lookup(ctx, root.ActiveLocation, candidate.RepresentationID)
		if errors.Is(err, storage.ErrKeyNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, ErrSuperseded
		}
		if err != nil {
			return nil, err
		}
		if !validChild(key, root, candidate, md) || !md.HasComplete() || md.Representation.BodyGeneration != ref.SelectedSourceVersion ||
			md.Representation.ValidatedEpoch != root.RevalidateEpoch || md.ExpiresAt <= r.Now().Unix() {
			return nil, ErrSuperseded
		}
		if _, allowed := xhttp.ParseCacheTime(protocol.ProtocolCacheTime, md.Headers); !allowed {
			return nil, ErrSuperseded
		}
		return md, nil
	}
	return nil, ErrSuperseded
}

// LookupDerived requires a currently selected source identity/version and its
// invalidation state; a long-lived output TTL alone cannot authorize a hit.
// Source metadata is sufficient: no source body file is opened here.
func (r *IndexRepository) LookupDerived(ctx context.Context, key object.ResourceKey, source *object.ID, version, transform string) (*LookupResult, error) {
	selector, err := managedSelector(source, version, transform)
	if err != nil {
		return nil, err
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	return r.lookupDerivedLocked(ctx, key, selector)
}

func (r *IndexRepository) lookupDerivedLocked(ctx context.Context, key object.ResourceKey, selector object.VariantRef) (*LookupResult, error) {
	root, err := r.load(ctx, key)
	if err != nil {
		return nil, err
	}
	result := &LookupResult{Resource: root}
	epochs, err := r.barriersFor(ctx, key)
	if err != nil {
		return result, err
	}
	if root.Resource.BarrierIncarnation != epochs.incarnation || root.Resource.PrefixHardEpoch != epochs.hard || root.Resource.PrefixSoftEpoch != epochs.soft {
		return result, storage.ErrKeyNotFound
	}
	source, err := r.currentSource(ctx, key, root.Resource, selector)
	if err != nil {
		if errors.Is(err, ErrSuperseded) {
			return result, storage.ErrKeyNotFound
		}
		return result, err
	}
	for _, ref := range root.Resource.Variants {
		if !sameSelector(ref, selector) {
			continue
		}
		md, err := r.lookup(ctx, root.Resource.ActiveLocation, ref.RepresentationID)
		if errors.Is(err, storage.ErrKeyNotFound) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !validChild(key, root.Resource, ref, md) {
			continue
		}
		if md.Representation.SourceBoundTTL {
			// This explicit lifetime contract follows a fresh validation of exactly the
			// same immutable source version. No content or stored ref revision changes.
			md = md.Clone()
			md.ExpiresAt, md.RespUnix = source.ExpiresAt, source.RespUnix
			md.Representation.ValidatedEpoch = source.Representation.ValidatedEpoch
			for _, name := range []string{"Date", "Age", "Cache-Control", "Expires", protocol.ProtocolCacheTime} {
				md.Headers.Del(name)
				if values, ok := source.Headers[name]; ok {
					md.Headers[name] = append([]string(nil), values...)
				}
			}
		}
		if md.ExpiresAt <= r.Now().Unix() || md.Representation.ValidatedEpoch != root.Resource.RevalidateEpoch {
			continue
		}
		if err := r.verifyChild(ctx, root.Resource.ActiveLocation, md); err != nil {
			if errors.Is(err, storage.ErrKeyNotFound) || errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, err
		}
		result.Representation, result.Reference, result.Fresh = md.Clone(), ref.Clone(), true
		return result, nil
	}
	return result, storage.ErrKeyNotFound
}

func validDerived(ref object.VariantRef, md *object.Metadata) bool {
	if _, allowed := xhttp.ParseCacheTime(protocol.ProtocolCacheTime, md.Headers); !allowed {
		return false
	}
	info := md.Representation
	return ref.SelectionKind == object.SelectionManaged && md.HasComplete() && sameID(info.SourceID, ref.SelectedSourceID) &&
		info.SourceVersion == ref.SelectedSourceVersion && len(info.TransformSpec) > 0 && json.Valid(info.TransformSpec) &&
		info.PolicyVersion != "" && info.EncoderVersion != "" && object.TransformFingerprint(info.TransformSpec, info.PolicyVersion, info.EncoderVersion) == ref.TransformFingerprint &&
		(info.StorageSelectorFingerprint == "" || info.StorageSelectorFingerprint == ref.SelectorFingerprint)
}

// Replacing one origin only retires products derived from that body identity.
// A 304 retains the origin ID/version and therefore retains its dependencies.
func originReplaced(existing, ref object.VariantRef, previous *object.VariantRef) bool {
	return existing.SelectionKind == object.SelectionHTTP && !sameID(existing.RepresentationID, ref.RepresentationID) &&
		(sameSelector(existing, ref) || previous != nil && sameReference(existing, *previous))
}
