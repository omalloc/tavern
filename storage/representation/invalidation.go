package representation

import (
	"context"
	"errors"
	"math"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

type InvalidationMode string

const (
	InvalidateSoft    InvalidationMode = "soft"
	InvalidateHard    InvalidationMode = "hard"
	maxPrefixBarriers                  = 4096
	maxPrefixBytes                     = 8192
	maxBarrierBytes                    = 1 << 20
)

var ErrBarrierLimit = storage.ErrInvalidationBudget

type barrierEpochs struct {
	hard, soft  uint64
	incarnation string
}

func WithBarrierLimit(limit int) Option {
	return func(r *IndexRepository) { r.barrierLimit = limit }
}

func canonicalPrefix(raw string) (string, error) {
	return canonicalBarrier(raw, false)
}

func canonicalBarrier(raw string, exact bool) (string, error) {
	if len(raw) > maxPrefixBytes {
		return "", ErrInvalidRecord
	}
	key, err := object.NewResourceKey(raw, "prefix", "")
	if err != nil {
		return "", ErrInvalidRecord
	}
	u, err := url.Parse(raw)
	if err != nil || (!exact && (u.RawQuery != "" || u.ForceQuery)) || u.Fragment != "" {
		return "", ErrInvalidRecord
	}
	u, _ = url.Parse(key.CanonicalURL())
	if !exact && !strings.HasSuffix(u.EscapedPath(), "/") {
		return "", ErrInvalidRecord
	}
	return u.String(), nil
}

func prefixMatches(prefix, resource string) bool {
	p, e1 := url.Parse(prefix)
	u, e2 := url.Parse(resource)
	return e1 == nil && e2 == nil && p.Scheme == u.Scheme && p.Host == u.Host && strings.HasPrefix(u.EscapedPath(), p.EscapedPath())
}

func (r *IndexRepository) loadCatalog(ctx context.Context) (*object.Metadata, error) {
	md, err := r.controls.LoadControl(ctx, object.BarrierCatalogID())
	if err != nil {
		return nil, err
	}
	if md == nil || md.Barriers == nil {
		return nil, ErrInvalidRecord
	}
	if md.Barriers.SchemaVersion != object.RepresentationSchemaVersion {
		return nil, ErrUnsupportedSchema
	}
	if !md.ValidControl() || md.Barriers.Incarnation == "" || len(md.Barriers.Entries) > r.barrierLimit || barrierBytes(md.Barriers) > maxBarrierBytes {
		return nil, ErrInvalidRecord
	}
	seen := make(map[barrierKey]struct{}, len(md.Barriers.Entries))
	namespaces := make(map[string]bool)
	if len(md.Barriers.QuerylessNamespaces) > 128 {
		return nil, ErrInvalidRecord
	}
	for _, namespace := range md.Barriers.QuerylessNamespaces {
		if namespace == "" || namespaces[namespace] {
			return nil, ErrInvalidRecord
		}
		namespaces[namespace] = true
	}
	for _, entry := range md.Barriers.Entries {
		prefix, err := canonicalBarrier(entry.CanonicalPrefix, entry.Exact)
		identity := barrierIdentity(entry)
		_, duplicate := seen[identity]
		if err != nil || prefix != entry.CanonicalPrefix || duplicate || entry.HardEpoch > md.Barriers.Revision || entry.SoftEpoch > md.Barriers.Revision ||
			(entry.HardEpoch == 0 && entry.SoftEpoch == 0) || entry.Namespace != "" && (!entry.Exact || !namespaces[entry.Namespace]) {
			return nil, ErrInvalidRecord
		}
		seen[identity] = struct{}{}
	}
	return md.Clone(), nil
}

func (r *IndexRepository) barriersFor(ctx context.Context, key object.ResourceKey) (barrierEpochs, error) {
	md, err := r.loadCatalog(ctx)
	if err != nil {
		return barrierEpochs{}, err
	}
	epochs := barrierEpochs{incarnation: md.Barriers.Incarnation}
	for _, entry := range md.Barriers.Entries {
		if entry.Namespace != "" && entry.Namespace != key.Namespace() {
			continue
		}
		if entry.Exact && entry.CanonicalPrefix == key.CanonicalURL() || !entry.Exact && prefixMatches(entry.CanonicalPrefix, key.CanonicalURL()) {
			epochs.hard = max(epochs.hard, entry.HardEpoch)
			epochs.soft = max(epochs.soft, entry.SoftEpoch)
		}
	}
	return epochs, nil
}

// InvalidatePrefix persists a directory barrier before any scan. It covers
// cold/unregistered resources too. Existing roots catch up on their next fill;
// lookups and commits check the barrier immediately. P2.3 wires public PURGE.
// A trailing slash is mandatory; escaped path separators remain distinct.
func (r *IndexRepository) InvalidatePrefix(ctx context.Context, raw string, mode InvalidationMode) error {
	return r.invalidateURLBarrier(ctx, raw, mode, false)
}

// InvalidateURL covers every namespace and cold isolation scope of an exact
// canonical public URI without needing to enumerate existing resource roots.
func (r *IndexRepository) InvalidateURL(ctx context.Context, raw string, mode InvalidationMode) error {
	return r.invalidateURLBarrier(ctx, raw, mode, true)
}

func (r *IndexRepository) invalidateURLBarrier(ctx context.Context, raw string, mode InvalidationMode, exact bool) error {
	if mode != InvalidateSoft && mode != InvalidateHard {
		return ErrInvalidRecord
	}
	prefix, err := canonicalBarrier(raw, exact)
	if err != nil {
		return err
	}
	r.barrierMu.Lock()
	defer r.barrierMu.Unlock()
	md, err := r.loadCatalog(ctx)
	if err != nil {
		return err
	}
	if md.Barriers.Revision == math.MaxUint64 {
		return ErrInvalidRecord
	}
	targets := []object.PrefixBarrier{{CanonicalPrefix: prefix, Exact: exact}}
	if exact {
		u, _ := url.Parse(prefix)
		if u.RawQuery != "" || u.ForceQuery {
			u.RawQuery, u.ForceQuery = "", false
			for _, namespace := range md.Barriers.QuerylessNamespaces {
				targets = append(targets, object.PrefixBarrier{CanonicalPrefix: u.String(), Exact: true, Namespace: namespace})
			}
		}
	}
	md.Barriers.Revision++
	for _, target := range targets {
		index := slices.IndexFunc(md.Barriers.Entries, func(entry object.PrefixBarrier) bool { return barrierIdentity(entry) == barrierIdentity(target) })
		if index < 0 {
			if len(md.Barriers.Entries) >= r.barrierLimit {
				return ErrBarrierLimit
			}
			md.Barriers.Entries = append(md.Barriers.Entries, target)
			index = len(md.Barriers.Entries) - 1
		}
		if mode == InvalidateHard {
			md.Barriers.Entries[index].HardEpoch = md.Barriers.Revision
		} else {
			md.Barriers.Entries[index].SoftEpoch = md.Barriers.Revision
		}
	}
	if barrierBytes(md.Barriers) > maxBarrierBytes {
		return ErrBarrierLimit
	}
	slices.SortFunc(md.Barriers.Entries, func(a, b object.PrefixBarrier) int { return strings.Compare(a.CanonicalPrefix, b.CanonicalPrefix) })
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.storeControl(ctx, md)
}

// Purge is the storage routing contract. Physical collection remains separate
// from the acknowledged logical barrier, so current readers can finish.
func (r *IndexRepository) Purge(ctx context.Context, raw string, control storage.PurgeControl) error {
	if control.Hard && control.MarkExpired {
		return storage.ErrInvalidPurge
	}
	if _, err := canonicalBarrier(raw, !control.Dir); err != nil {
		return storage.ErrInvalidPurge
	}
	mode := InvalidateSoft
	if control.Hard {
		mode = InvalidateHard
	}
	if control.Dir {
		return r.InvalidatePrefix(ctx, raw, mode)
	}
	return r.InvalidateURL(ctx, raw, mode)
}

var _ storage.RepresentationPurger = (*IndexRepository)(nil)

func barrierBytes(catalog *object.BarrierCatalog) int {
	size := len(catalog.Incarnation) + 32
	for _, namespace := range catalog.QuerylessNamespaces {
		size += len(namespace) + 16
	}
	for _, entry := range catalog.Entries {
		size += len(entry.CanonicalPrefix) + len(entry.Namespace) + 32
	}
	return size
}

type barrierKey struct {
	url, namespace string
	exact          bool
}

func barrierIdentity(entry object.PrefixBarrier) barrierKey {
	return barrierKey{entry.CanonicalPrefix, entry.Namespace, entry.Exact}
}

// BindQuerylessNamespace persists the public-URI mapping before fills begin.
// It survives disabled reads, configuration changes and process restarts.
func (r *IndexRepository) BindQuerylessNamespace(ctx context.Context, namespace string) error {
	if namespace == "" {
		return ErrInvalidRecord
	}
	r.barrierMu.Lock()
	defer r.barrierMu.Unlock()
	md, err := r.loadCatalog(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(md.Barriers.QuerylessNamespaces, namespace) {
		return nil
	}
	if len(md.Barriers.QuerylessNamespaces) >= 128 || md.Barriers.Revision == math.MaxUint64 {
		return ErrBarrierLimit
	}
	md.Barriers.QuerylessNamespaces = append(md.Barriers.QuerylessNamespaces, namespace)
	slices.Sort(md.Barriers.QuerylessNamespaces)
	// Existing URI barriers also apply when enabling this mapping over an
	// older cache. Do not let prior acknowledged PURGEs disappear on upgrade.
	for _, entry := range slices.Clone(md.Barriers.Entries) {
		if !entry.Exact || entry.Namespace != "" {
			continue
		}
		u, _ := url.Parse(entry.CanonicalPrefix)
		if u.RawQuery == "" && !u.ForceQuery {
			continue
		}
		u.RawQuery, u.ForceQuery = "", false
		mapped := object.PrefixBarrier{CanonicalPrefix: u.String(), Exact: true, Namespace: namespace, HardEpoch: entry.HardEpoch, SoftEpoch: entry.SoftEpoch}
		index := slices.IndexFunc(md.Barriers.Entries, func(candidate object.PrefixBarrier) bool {
			return barrierIdentity(candidate) == barrierIdentity(mapped)
		})
		if index < 0 {
			if len(md.Barriers.Entries) >= r.barrierLimit {
				return ErrBarrierLimit
			}
			md.Barriers.Entries = append(md.Barriers.Entries, mapped)
		} else {
			md.Barriers.Entries[index].HardEpoch = max(md.Barriers.Entries[index].HardEpoch, mapped.HardEpoch)
			md.Barriers.Entries[index].SoftEpoch = max(md.Barriers.Entries[index].SoftEpoch, mapped.SoftEpoch)
		}
	}
	md.Barriers.Revision++
	if barrierBytes(md.Barriers) > maxBarrierBytes {
		return ErrBarrierLimit
	}
	return r.storeControl(ctx, md)
}

// Invalidate persists even for a cold resource. Hard invalidation cuts logical
// references only; reader-safe physical collection is a later lifecycle step.
func (r *IndexRepository) Invalidate(ctx context.Context, key object.ResourceKey, mode InvalidationMode) error {
	if !key.Valid() || (mode != InvalidateSoft && mode != InvalidateHard) {
		return ErrInvalidRecord
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	root, _, err := r.rootForFill(ctx, key)
	if err != nil {
		return err
	}
	if root.Resource.Revision == math.MaxUint64 {
		return ErrInvalidRecord
	}
	if mode == InvalidateHard {
		if root.Resource.PurgeEpoch == math.MaxUint64 {
			return ErrInvalidRecord
		}
		root.Resource.PurgeEpoch++
		root.Resource.Variants = nil
	} else {
		if root.Resource.RevalidateEpoch == math.MaxUint64 {
			return ErrInvalidRecord
		}
		root.Resource.RevalidateEpoch++
	}
	root.Resource.Revision++
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.storeControlSync(ctx, root)
}

// rootForFill runs with barrier read lock and resource lock. It never reads a
// body. A prefix invalidation is applied before issuing a new fill identity.
func (r *IndexRepository) rootForFill(ctx context.Context, key object.ResourceKey) (*object.Metadata, bool, error) {
	epochs, err := r.barriersFor(ctx, key)
	if err != nil {
		return nil, false, err
	}
	root, err := r.load(ctx, key)
	changed := false
	if errors.Is(err, storage.ErrKeyNotFound) {
		root = &object.Metadata{ID: key.ID(), RecordKind: object.RecordResource, Resource: &object.ResourceRecord{
			SchemaVersion: object.RepresentationSchemaVersion, ResourceID: key.ResourceID(), CanonicalURL: key.CanonicalURL(),
			Namespace: key.Namespace(), Incarnation: uuid.NewString(), ActiveLocation: r.initialLocation}}
		changed, err = true, nil
	}
	if err != nil {
		return nil, false, err
	}
	record := root.Resource
	if record.BarrierIncarnation == "" {
		record.BarrierIncarnation, changed = epochs.incarnation, true
	} else if record.BarrierIncarnation != epochs.incarnation {
		return nil, false, ErrInvalidRecord
	}
	// Lower barriers indicate missing/corrupt control history. Never reset it.
	if epochs.hard < record.PrefixHardEpoch || epochs.soft < record.PrefixSoftEpoch {
		return nil, false, ErrInvalidRecord
	}
	if epochs.hard > record.PrefixHardEpoch {
		if record.PurgeEpoch == math.MaxUint64 {
			return nil, false, ErrInvalidRecord
		}
		record.PurgeEpoch++
		record.Variants = nil
		changed = true
	}
	if epochs.soft > record.PrefixSoftEpoch {
		if record.RevalidateEpoch == math.MaxUint64 {
			return nil, false, ErrInvalidRecord
		}
		record.RevalidateEpoch++
		changed = true
	}
	record.PrefixHardEpoch, record.PrefixSoftEpoch = epochs.hard, epochs.soft
	return root, changed, nil
}

func (r *IndexRepository) currentBarriers(ctx context.Context, state *IndexState) (bool, error) {
	epochs, err := r.barriersFor(ctx, state.key)
	if err != nil {
		return false, err
	}
	return epochs.incarnation == state.root.Resource.BarrierIncarnation && epochs.hard == state.root.Resource.PrefixHardEpoch && epochs.soft == state.root.Resource.PrefixSoftEpoch, nil
}

func (r *IndexRepository) initializeCatalog(ctx context.Context) error {
	_, err := r.loadCatalog(ctx)
	if !errors.Is(err, storage.ErrKeyNotFound) {
		return err
	}
	return r.storeControl(ctx, &object.Metadata{ID: object.BarrierCatalogID(), RecordKind: object.RecordBarriers,
		Barriers: &object.BarrierCatalog{SchemaVersion: object.RepresentationSchemaVersion, Incarnation: uuid.NewString()}})
}
