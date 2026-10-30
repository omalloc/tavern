package representation

import (
	"context"
	"math"
	"net/url"
	"strings"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
)

// BarrierCompaction reports candidate entries examined, rather than an elapsed
// age. A candidate is removed only when a directory barrier dominates both of
// its epochs for every URI it could match.
type BarrierCompaction struct{ Scanned, Removed int }

// CompactBarriers preserves the max hard/soft epochs for every possible URI,
// including cold namespaces and in-flight tickets not enumerable from roots.
// Neither catalog incarnation nor epoch values are reset. Uncovered history is
// retained regardless of age; this is deliberately not general tombstone GC.
func (r *IndexRepository) CompactBarriers(ctx context.Context, budget int) (result BarrierCompaction, failure error) {
	defer func() { pkgmetrics.RecordBarrierCompaction(result.Scanned, result.Removed, failure != nil) }()
	if budget <= 0 {
		return BarrierCompaction{}, ErrInvalidRecord
	}
	if err := ctx.Err(); err != nil {
		return BarrierCompaction{}, err
	}
	r.barrierMu.Lock()
	defer r.barrierMu.Unlock()
	md, err := r.loadCatalog(ctx)
	if err != nil {
		return BarrierCompaction{}, err
	}
	result, entries, next, err := compactBarrierEntries(ctx, md.Barriers.Entries, r.barrierCompactCursor, min(budget, 128))
	if err != nil {
		return BarrierCompaction{}, err
	}
	if result.Removed > 0 {
		if md.Barriers.Revision == math.MaxUint64 {
			return BarrierCompaction{}, ErrInvalidRecord
		}
		copy := md.Clone()
		copy.Barriers.Entries = entries
		copy.Barriers.Revision++
		if err = ctx.Err(); err != nil {
			return BarrierCompaction{}, err
		}
		if err = r.storeControl(ctx, copy); err != nil {
			return BarrierCompaction{}, err
		}
	}
	r.barrierCompactCursor = next
	return result, nil
}

type barrierCoverage struct{ scheme, host, path string }

func compactBarrierEntries(ctx context.Context, entries []object.PrefixBarrier, cursor, budget int) (BarrierCompaction, []object.PrefixBarrier, int, error) {
	result := BarrierCompaction{}
	if len(entries) == 0 {
		return result, nil, 0, nil
	}
	parsed := make([]barrierCoverage, len(entries))
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, nil, 0, err
		}
		u, err := url.Parse(entry.CanonicalPrefix)
		if err != nil {
			return result, nil, 0, ErrInvalidRecord
		}
		parsed[i] = barrierCoverage{u.Scheme, u.Host, u.EscapedPath()}
	}
	cursor %= len(entries)
	remove := make([]bool, len(entries))
	for result.Scanned < min(budget, len(entries)) {
		if err := ctx.Err(); err != nil {
			return result, nil, 0, err
		}
		index := (cursor + result.Scanned) % len(entries)
		candidate := entries[index]
		where := parsed[index]
		result.Scanned++
		for i, cover := range entries {
			if i == index || cover.Exact || cover.HardEpoch < candidate.HardEpoch || cover.SoftEpoch < candidate.SoftEpoch {
				continue
			}
			ancestor := parsed[i]
			if ancestor.scheme != where.scheme || ancestor.host != where.host || !strings.HasPrefix(where.path, ancestor.path) {
				continue
			}
			// Directory coverage strictly moves toward an ancestor; duplicate directory
			// entries must not mutually remove each other. Exact URIs may equal the
			// directory path, including a query, since that prefix covers every query.
			if !candidate.Exact && len(ancestor.path) >= len(where.path) {
				continue
			}
			remove[index] = true
			result.Removed++
			break
		}
	}
	oldNext := (cursor + result.Scanned) % len(entries)
	retained := make([]object.PrefixBarrier, 0, len(entries)-result.Removed)
	next := 0
	for i, entry := range entries {
		if !remove[i] {
			if i < oldNext {
				next++
			}
			retained = append(retained, entry)
		}
	}
	if len(retained) > 0 {
		next %= len(retained)
	} else {
		next = 0
	}
	return result, retained, next, nil
}

// CompactBarriers bounds candidates across control owners and rotates the next
// home between calls. Each catalog has at most the validated barrier limit and
// each owner examines at most 128 candidates per pass.
func (m *Manager) CompactBarriers(ctx context.Context, budget int) (BarrierCompaction, error) {
	result := BarrierCompaction{}
	if budget <= 0 {
		return result, ErrInvalidRecord
	}
	m.gcMu.Lock()
	defer m.gcMu.Unlock()
	for visited := 0; visited < len(m.buckets) && result.Scanned < budget; visited++ {
		bucket := m.buckets[m.compactNext]
		m.compactNext = (m.compactNext + 1) % len(m.buckets)
		partial, err := m.repos[bucket.ID()].CompactBarriers(ctx, min(128, budget-result.Scanned))
		result.Scanned += partial.Scanned
		result.Removed += partial.Removed
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
