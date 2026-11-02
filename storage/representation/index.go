// Package representation coordinates protected indexes, fill tickets,
// invalidation barriers and publication independently of HTTP response assembly.
package representation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

var (
	ErrInvalidRecord     = errors.New("invalid representation index record")
	ErrUnsupportedSchema = errors.New("unsupported representation schema")
	ErrSelectorLimit     = errors.New("representation selector limit exceeded")
	ErrSuperseded        = errors.New("representation update superseded")
)

// LookupFunc loads response metadata at the root's fixed active location.
// It must not dynamically select another location on a miss.
type LookupFunc func(context.Context, string, *object.ID) (*object.Metadata, error)

type indexShard struct {
	mu         sync.Mutex
	pending    map[object.IDHash]map[string]*Reservation
	readers    map[dataIdentity]int
	writers    map[dataIdentity]int
	migrations map[object.IDHash]*migrationState
}

type IndexRepository struct {
	controls             storage.ControlStore
	initialLocation      string
	lookup               LookupFunc
	durable              bool
	writeback            bool
	dataBuckets          []storage.Bucket
	durableBuckets       map[string]storage.DurableRepresentationBucket
	barrierMu            sync.RWMutex // lock order: barrier, then resource shard
	barrierLimit         int
	barrierCompactCursor int
	buckets              map[string]storage.Bucket
	lifecycle            bool
	now                  func() time.Time
	shards               [256]indexShard
}

type Option func(*IndexRepository)

func WithClock(now func() time.Time) Option {
	return func(r *IndexRepository) {
		if now != nil {
			r.now = now
		}
	}
}

// NewIndexRepository requires one repository owner per control store. Its locks
// serialize in-process read/modify/write operations, not multiple processes.
// ControlStore alone does not enable persistent v2 caching in the HTTP path.
func NewIndexRepository(controls storage.ControlStore, initialLocation string, lookup LookupFunc, opts ...Option) (*IndexRepository, error) {
	if controls == nil {
		return nil, storage.ErrControlUnsupported
	}
	if initialLocation == "" || lookup == nil {
		return nil, ErrInvalidRecord
	}
	r := &IndexRepository{controls: controls, initialLocation: initialLocation, lookup: lookup, now: time.Now, barrierLimit: maxPrefixBarriers}
	for _, opt := range opts {
		opt(r)
	}
	if r.barrierLimit <= 0 || r.barrierLimit > maxPrefixBarriers {
		return nil, ErrInvalidRecord
	}
	if r.durable {
		if err := r.checkDurability(); err != nil {
			return nil, err
		}
	}
	if len(r.dataBuckets) > 0 {
		r.buckets = make(map[string]storage.Bucket)
		for _, bucket := range r.dataBuckets {
			if bucket == nil || r.buckets[bucket.ID()] != nil {
				return nil, ErrInvalidRecord
			}
			r.buckets[bucket.ID()] = bucket
		}
	}
	if err := r.initializeCatalog(context.Background()); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *IndexRepository) shard(key object.ResourceKey) *indexShard {
	return &r.shards[key.ID().Hash()[0]]
}

func (r *IndexRepository) Now() time.Time { return r.now() }

// Load returns an owned root. Unknown versions and malformed records are misses;
// Update distinguishes them so it cannot overwrite unrecognized purge state.
func (r *IndexRepository) Load(ctx context.Context, key object.ResourceKey) (*object.Metadata, error) {
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	md, err := r.load(ctx, key)
	if errors.Is(err, ErrUnsupportedSchema) || errors.Is(err, ErrInvalidRecord) {
		return nil, storage.ErrKeyNotFound
	}
	return md, err
}

func (r *IndexRepository) load(ctx context.Context, key object.ResourceKey) (*object.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !key.Valid() {
		return nil, ErrInvalidRecord
	}
	md, err := r.controls.LoadControl(ctx, key.ID())
	if errors.Is(err, os.ErrNotExist) {
		return nil, storage.ErrKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	if md == nil {
		return nil, storage.ErrKeyNotFound
	}
	if md.RecordKind != object.RecordResource || md.Resource == nil || md.Representation != nil || md.Barriers != nil {
		return nil, ErrInvalidRecord
	}
	if md.Resource.SchemaVersion != object.RepresentationSchemaVersion {
		return nil, ErrUnsupportedSchema
	}
	record := md.Resource
	if !sameID(md.ID, key.ID()) || record.ResourceID != key.ResourceID() ||
		record.CanonicalURL != key.CanonicalURL() || record.Namespace != key.Namespace() ||
		record.Incarnation == "" || record.ActiveLocation == "" || len(md.VirtualKey) != 0 ||
		len(md.Headers) != 0 || md.Size != 0 || md.BlockSize != 0 || md.Chunks.Count() != 0 || md.Parts.Count() != 0 || md.Code != 0 || md.Flags != object.FlagCache {
		return nil, ErrInvalidRecord
	}
	return md.Clone(), nil
}

// Update rereads the latest root under the resource lock. The callback only
// changes that owned record and must not perform network I/O or call back into
// this repository. Child metadata must exist before a reference is published.
func (r *IndexRepository) Update(ctx context.Context, key object.ResourceKey, update func(*object.ResourceRecord) error) (*object.Metadata, error) {
	if !key.Valid() {
		return nil, ErrInvalidRecord
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	return r.updateLocked(ctx, key, update)
}

func (r *IndexRepository) updateLocked(ctx context.Context, key object.ResourceKey, update func(*object.ResourceRecord) error) (*object.Metadata, error) {
	md, _, err := r.rootForFill(ctx, key)
	if err != nil {
		return nil, err
	}
	before := md.Resource.Clone()
	if update != nil {
		if err := update(md.Resource); err != nil {
			return nil, err
		}
	}
	record := md.Resource
	if record.SchemaVersion != object.RepresentationSchemaVersion || record.ResourceID != key.ResourceID() ||
		record.CanonicalURL != key.CanonicalURL() || record.Namespace != key.Namespace() ||
		record.Incarnation != before.Incarnation || record.ActiveLocation == "" || record.PurgeEpoch < before.PurgeEpoch ||
		record.RevalidateEpoch < before.RevalidateEpoch || record.RoutingEpoch < before.RoutingEpoch ||
		(record.ActiveLocation != before.ActiveLocation && record.RoutingEpoch == before.RoutingEpoch) || before.Revision == math.MaxUint64 {
		return nil, ErrInvalidRecord
	}
	if record.PrefixHardEpoch != before.PrefixHardEpoch || record.PrefixSoftEpoch != before.PrefixSoftEpoch || record.BarrierIncarnation != before.BarrierIncarnation {
		return nil, ErrInvalidRecord
	}
	// Managed references can only be added/replaced through Publish, which
	// checks complete metadata and invalidation epochs before committing them.
	if r.durable || r.lifecycle {
		if len(record.Variants) != len(before.Variants) || record.ActiveLocation != before.ActiveLocation {
			return nil, ErrInvalidRecord
		}
		for i := range record.Variants {
			if !sameReference(record.Variants[i], before.Variants[i]) {
				return nil, ErrInvalidRecord
			}
		}
	}
	for _, ref := range record.Variants {
		if err := validateReference(ref); err != nil {
			return nil, err
		}
		child, err := r.lookup(ctx, record.ActiveLocation, ref.RepresentationID)
		if err != nil {
			return nil, fmt.Errorf("unreadable representation: %w", err)
		}
		if !validChild(key, record, ref, child) {
			return nil, ErrInvalidRecord
		}
	}
	record.Revision = before.Revision + 1
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A callback retaining its argument cannot mutate the stored root later.
	if r.durable && r.durableBuckets[record.ActiveLocation] == nil {
		return nil, storage.ErrDurabilityUnsupported
	}
	store := r.storeControl
	if record.PurgeEpoch != before.PurgeEpoch || record.RevalidateEpoch != before.RevalidateEpoch {
		store = r.storeControlSync
	}
	if err := store(ctx, md); err != nil {
		return nil, err
	}
	return md.Clone(), nil
}

type LookupResult struct {
	Resource       *object.Metadata
	Representation *object.Metadata
	Reference      object.VariantRef
	Fresh          bool
	Lease          *ReadLease
}

// Lookup selects by each reference's own Vary set, then response Date and a
// stable ID order. Freshness is checked only after choosing a representation;
// an older fresh candidate must not hide a newer expired response.
// A miss may still return Resource, but never a usable Representation.
func (r *IndexRepository) Lookup(ctx context.Context, key object.ResourceKey, header http.Header) (*LookupResult, error) {
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	return r.lookupLocked(ctx, key, header)
}

func (r *IndexRepository) lookupLocked(ctx context.Context, key object.ResourceKey, header http.Header) (*LookupResult, error) {
	return r.lookupSelected(ctx, key, header, true)
}

// LookupOriginMetadata selects a source version without opening its data files.
// It grants no permission to read bytes: acquire LookupRead before doing so.
func (r *IndexRepository) LookupOriginMetadata(ctx context.Context, key object.ResourceKey, header http.Header) (*LookupResult, error) {
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	return r.lookupSelected(ctx, key, header, false)
}

func (r *IndexRepository) lookupSelected(ctx context.Context, key object.ResourceKey, header http.Header, verifyData bool) (*LookupResult, error) {
	root, err := r.load(ctx, key)
	if errors.Is(err, ErrUnsupportedSchema) || errors.Is(err, ErrInvalidRecord) {
		err = storage.ErrKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	result := &LookupResult{Resource: root}
	epochs, err := r.barriersFor(ctx, key)
	if err != nil {
		return result, err
	}
	if root.Resource.BarrierIncarnation != epochs.incarnation || root.Resource.PrefixHardEpoch != epochs.hard || root.Resource.PrefixSoftEpoch > epochs.soft {
		return result, storage.ErrKeyNotFound
	}
	var candidates []object.VariantRef
	groups := make(map[string]varycontrol.Snapshot)
	for _, ref := range root.Resource.Variants {
		if ref.SelectionKind != object.SelectionHTTP || validateReference(ref) != nil {
			continue
		}
		group := stringsKey(ref.VaryFields)
		actual, ok := groups[group]
		if !ok {
			actual, err = varycontrol.Capture(ref.VaryFields, header)
			if err != nil {
				continue
			}
			groups[group] = actual
		}
		if varycontrol.Fingerprint(actual) == ref.SelectorFingerprint && equalSnapshot(actual, ref.SelectorSnapshot) {
			candidates = append(candidates, ref)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ResponseDate != candidates[j].ResponseDate {
			return candidates[i].ResponseDate > candidates[j].ResponseDate
		}
		return candidates[i].RepresentationID.HashStr() < candidates[j].RepresentationID.HashStr()
	})
	for _, ref := range candidates {
		child, err := r.lookup(ctx, root.Resource.ActiveLocation, ref.RepresentationID)
		if errors.Is(err, storage.ErrKeyNotFound) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !validChild(key, root.Resource, ref, child) {
			continue
		}
		if verifyData {
			if err := r.verifyChild(ctx, root.Resource.ActiveLocation, child); err != nil {
				if errors.Is(err, storage.ErrKeyNotFound) || errors.Is(err, os.ErrNotExist) {
					continue
				}
				return result, err
			}
		}
		result.Representation, result.Reference = child.Clone(), ref.Clone()
		result.Fresh = child.ExpiresAt > r.now().Unix() && child.Representation.ValidatedEpoch == root.Resource.RevalidateEpoch && root.Resource.PrefixSoftEpoch == epochs.soft
		return result, nil
	}
	return result, storage.ErrKeyNotFound
}

func NewHTTPSelector(fields []string, header http.Header) (object.VariantRef, error) {
	parsed, wildcard, err := varycontrol.Parse(fields...)
	if err != nil {
		return object.VariantRef{}, err
	}
	if wildcard {
		return object.VariantRef{}, varycontrol.ErrWildcard
	}
	snapshot, err := varycontrol.Capture(parsed, header)
	if err != nil {
		return object.VariantRef{}, err
	}
	return object.VariantRef{SelectionKind: object.SelectionHTTP, VaryFields: parsed,
		SelectorSnapshot: snapshot, SelectorFingerprint: varycontrol.Fingerprint(snapshot)}, nil
}

func validateSelector(ref object.VariantRef) error {
	if ref.SelectionKind == object.SelectionManaged {
		if len(ref.VaryFields) != 0 || len(ref.SelectorSnapshot.Fields) != 0 || ref.SelectorSnapshot.Version != 0 {
			return ErrInvalidRecord
		}
		expected, err := object.ManagedSelectorFingerprint(ref.SelectedSourceID, ref.SelectedSourceVersion, ref.TransformFingerprint)
		if err != nil || expected != ref.SelectorFingerprint {
			return ErrInvalidRecord
		}
		return nil
	}
	if ref.SelectionKind != object.SelectionHTTP || ref.SelectedSourceID != nil || ref.SelectedSourceVersion != "" || ref.TransformFingerprint != "" {
		return ErrInvalidRecord
	}
	fields, wildcard, err := varycontrol.Parse(ref.VaryFields...)
	if err != nil || wildcard || !slices.Equal(fields, ref.VaryFields) || len(fields) != len(ref.SelectorSnapshot.Fields) {
		return ErrInvalidRecord
	}
	header := make(http.Header)
	for i, field := range ref.SelectorSnapshot.Fields {
		if field.Name != fields[i] || (!field.Present && len(field.Values) != 0) {
			return ErrInvalidRecord
		}
		if field.Present {
			header[field.Name] = slices.Clone(field.Values)
		}
	}
	if !varycontrol.Match(ref.SelectorSnapshot, header) || varycontrol.Fingerprint(ref.SelectorSnapshot) != ref.SelectorFingerprint {
		return ErrInvalidRecord
	}
	return nil
}

func validateReference(ref object.VariantRef) error {
	if ref.RepresentationID == nil || !ref.RepresentationID.IsRepresentation() {
		return ErrInvalidRecord
	}
	return validateSelector(ref)
}

func validChild(key object.ResourceKey, root *object.ResourceRecord, ref object.VariantRef, md *object.Metadata) bool {
	if md == nil || !md.IsResponse() || md.RecordKind != object.RecordRepresentation || !sameID(md.ID, ref.RepresentationID) || !md.HasReadableChunks() || md.Code != http.StatusOK {
		return false
	}
	if md.Size > math.MaxInt64 || md.BlockSize > math.MaxInt64 {
		return false
	}
	// Stored headers always describe the full representation. A partial origin
	// response publishes only confirmed chunks, with a known complete size.
	length, err := strconv.ParseUint(md.Headers.Get("Content-Length"), 10, 64)
	if err != nil || length != md.Size || len(md.Headers.Values("Content-Length")) != 1 ||
		md.Headers.Get("Content-Range") != "" || md.Flags != object.FlagCache {
		return false
	}
	info := md.Representation
	if info.Revision != ref.Revision {
		return false
	}
	if info.ResourceID != root.ResourceID || info.Incarnation != root.Incarnation || info.PurgeEpoch != root.PurgeEpoch ||
		info.ValidatedEpoch > root.RevalidateEpoch || info.BodyGeneration == "" {
		return false
	}
	switch info.Kind {
	case object.RepresentationOrigin:
		if ref.SelectionKind != object.SelectionHTTP || info.SourceID != nil || info.SourceVersion != "" || len(info.TransformSpec) != 0 {
			return false
		}
	case object.RepresentationDerived:
		if !validDerived(ref, md) {
			return false
		}
	default:
		return false
	}
	storageSelector := info.StorageSelectorFingerprint
	if storageSelector == "" {
		storageSelector = ref.SelectorFingerprint
	}
	expectedID, err := key.RepresentationID(root.Incarnation, root.PurgeEpoch, storageSelector, info.BodyGeneration)
	if err != nil || !sameID(md.ID, expectedID) {
		return false
	}
	fields, wildcard, err := varycontrol.Parse(md.Headers.Values("Vary")...)
	if err != nil || wildcard || (info.Kind == object.RepresentationOrigin && !slices.Equal(fields, ref.VaryFields)) {
		return false
	}
	date := md.RespUnix
	if value := md.Headers.Get("Date"); value != "" {
		parsed, err := http.ParseTime(value)
		if err != nil {
			return false
		}
		date = parsed.Unix()
	}
	return ref.ResponseDate == date
}

func sameID(a, b *object.ID) bool {
	return a != nil && b != nil && a.Hash() == b.Hash() && a.Path() == b.Path() && a.Ext() == b.Ext()
}
func equalSnapshot(a, b varycontrol.Snapshot) bool {
	if a.Version != b.Version || len(a.Fields) != len(b.Fields) {
		return false
	}
	for i, left := range a.Fields {
		right := b.Fields[i]
		if left.Name != right.Name || left.Present != right.Present || !slices.Equal(left.Values, right.Values) {
			return false
		}
	}
	return true
}
func sameSelector(a, b object.VariantRef) bool {
	if a.SelectionKind == object.SelectionManaged || b.SelectionKind == object.SelectionManaged {
		return a.SelectionKind == b.SelectionKind && a.SelectorFingerprint == b.SelectorFingerprint && a.TransformFingerprint == b.TransformFingerprint &&
			sameID(a.SelectedSourceID, b.SelectedSourceID) && a.SelectedSourceVersion == b.SelectedSourceVersion
	}
	return a.SelectionKind == b.SelectionKind && a.SelectorFingerprint == b.SelectorFingerprint &&
		slices.Equal(a.VaryFields, b.VaryFields) && equalSnapshot(a.SelectorSnapshot, b.SelectorSnapshot)
}

// NUL cannot occur in a legal field name, so group boundaries are unambiguous.
func stringsKey(fields []string) string {
	var key string
	for _, field := range fields {
		key += field + "\x00"
	}
	return key
}
