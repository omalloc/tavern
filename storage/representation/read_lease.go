package representation

import (
	"context"
	"net/http"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

type dataIdentity struct {
	id       object.IDHash
	location string
}

// ReadLease pins exactly the location/physical IDs selected under the resource
// lock. Metadata-only lookups do not acquire one; body readers must.
type ReadLease struct {
	repo    *IndexRepository
	key     object.ResourceKey
	objects []dataIdentity
	once    sync.Once
	closed  bool // protected by the resource shard
}

func (l *ReadLease) Location() string {
	if l == nil || len(l.objects) == 0 {
		return ""
	}
	return l.objects[0].location
}

func WithLifecycle(buckets ...storage.Bucket) Option {
	return func(r *IndexRepository) {
		r.lifecycle = true
		r.dataBuckets = append([]storage.Bucket(nil), buckets...)
	}
}

func (r *IndexRepository) pinLocked(key object.ResourceKey, location string, ids ...*object.ID) *ReadLease {
	shard := r.shard(key)
	if shard.readers == nil {
		shard.readers = make(map[dataIdentity]int)
	}
	lease := &ReadLease{repo: r, key: key}
	for _, id := range ids {
		identity := dataIdentity{id.Hash(), location}
		shard.readers[identity]++
		lease.objects = append(lease.objects, identity)
	}
	return lease
}

func (l *ReadLease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		shard := l.repo.shard(l.key)
		shard.mu.Lock()
		defer shard.mu.Unlock()
		l.closed = true
		for _, id := range l.objects {
			shard.readers[id]--
			if shard.readers[id] <= 0 {
				delete(shard.readers, id)
			}
		}
	})
	return nil
}

// Duplicate gives a background reader independent ownership of the same bytes.
func (l *ReadLease) Duplicate() *ReadLease {
	if l == nil {
		return nil
	}
	shard := l.repo.shard(l.key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if l.closed {
		return nil
	}
	copy := &ReadLease{repo: l.repo, key: l.key, objects: append([]dataIdentity(nil), l.objects...)}
	for _, id := range copy.objects {
		shard.readers[id]++
	}
	return copy
}

func (r *IndexRepository) LookupRead(ctx context.Context, key object.ResourceKey, header http.Header) (*LookupResult, error) {
	if !key.Valid() {
		return nil, ErrInvalidRecord
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	result, err := r.lookupLocked(ctx, key, header)
	if err == nil {
		result.Lease = r.pinLocked(key, result.Resource.Resource.ActiveLocation, result.Representation.ID)
	}
	return result, err
}

func (r *IndexRepository) LookupDerivedRead(ctx context.Context, key object.ResourceKey, source *object.ID, version, transform string) (*LookupResult, error) {
	if !key.Valid() {
		return nil, ErrInvalidRecord
	}
	selector, err := managedSelector(source, version, transform)
	if err != nil {
		return nil, err
	}
	r.barrierMu.RLock()
	defer r.barrierMu.RUnlock()
	shard := r.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	result, err := r.lookupDerivedLocked(ctx, key, selector)
	if err == nil {
		result.Lease = r.pinLocked(key, result.Resource.Resource.ActiveLocation, result.Representation.ID)
	}
	return result, err
}
