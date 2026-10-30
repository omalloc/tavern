package storage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

type representationRegistry struct {
	mu     sync.RWMutex
	owners map[string]storagev1.RepresentationPurger
	fills  map[*legacyFill]struct{}
}

func (r *representationRegistry) register(name string, purger storagev1.RepresentationPurger) error {
	if name == "" || purger == nil {
		return storagev1.ErrControlUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owners == nil {
		r.owners = make(map[string]storagev1.RepresentationPurger)
	}
	if _, exists := r.owners[name]; exists {
		return fmt.Errorf("representation control owner %q already registered", name)
	}
	r.owners[name] = purger
	return nil
}

func canonicalPurge(raw string, directory bool) (string, error) {
	key, err := object.NewResourceKey(raw, "purge", "")
	if err != nil {
		return "", storagev1.ErrInvalidPurge
	}
	if directory {
		u, _ := url.Parse(key.CanonicalURL())
		if u.RawQuery != "" || u.ForceQuery || !strings.HasSuffix(u.EscapedPath(), "/") {
			return "", storagev1.ErrInvalidPurge
		}
	}
	return key.CanonicalURL(), nil
}

func purgeMatches(target, raw string, directory bool) bool {
	canonical, err := canonicalPurge(raw, false)
	if err != nil {
		return false
	}
	if !directory {
		return target == canonical
	}
	p, _ := url.Parse(target)
	u, _ := url.Parse(canonical)
	return p.Scheme == u.Scheme && p.Host == u.Host && strings.HasPrefix(u.EscapedPath(), p.EscapedPath())
}

// purge serializes legacy local writes, persists all registered v2 barriers
// first, then visits actual legacy IDs across every bucket. V2 physical bytes
// are intentionally left to the reader-aware coordinator's collection protocol.
func (r *representationRegistry) purge(ctx context.Context, buckets []storagev1.Bucket, raw string, control storagev1.PurgeControl) error {
	if control.Hard && control.MarkExpired {
		return storagev1.ErrInvalidPurge
	}
	target, err := canonicalPurge(raw, control.Dir)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	handled := false
	for fill := range r.fills {
		if purgeMatches(target, fill.url, control.Dir) {
			fill.invalid = true
			handled = true
		}
	}
	var failures []error
	for name, owner := range r.owners {
		if err := owner.Purge(ctx, target, control); err != nil {
			failures = append(failures, fmt.Errorf("representation owner %s: %w", name, err))
		} else {
			handled = true
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	seen := make(map[string]struct{})
	for _, bucket := range buckets {
		if bucket == nil {
			continue
		}
		if _, duplicate := seen[bucket.ID()]; duplicate {
			continue
		}
		seen[bucket.ID()] = struct{}{}
		err := bucket.Iterate(ctx, func(md *object.Metadata) error {
			if md == nil || md.ID == nil || !md.IsLegacy() || !purgeMatches(target, md.ID.Path(), control.Dir) {
				return nil
			}
			handled = true
			var err error
			if control.Hard {
				err = bucket.DiscardWithMetadata(ctx, md)
			} else {
				copy := md.Clone()
				copy.ExpiresAt = time.Now().Unix() - 1
				err = bucket.Store(ctx, copy)
			}
			if err != nil && !errors.Is(err, storagev1.ErrKeyNotFound) && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, err)
			}
			// Backends historically ignored callback stop/error results. Collect
			// failures explicitly so partial scans never report false success.
			return nil
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	if !handled {
		return storagev1.ErrKeyNotFound
	}
	return nil
}

type legacyFill struct {
	owner           *representationRegistry
	url             string
	invalid, closed bool
}

var _ storagev1.LegacyFillLease = (*legacyFill)(nil)

func (r *representationRegistry) begin(ctx context.Context, raw string) (storagev1.LegacyFillLease, error) {
	canonical, err := canonicalPurge(raw, false)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beginLocked(canonical), nil
}

func (r *representationRegistry) beginLocked(canonical string) *legacyFill {
	if r.fills == nil {
		r.fills = make(map[*legacyFill]struct{})
	}
	fill := &legacyFill{owner: r, url: canonical}
	r.fills[fill] = struct{}{}
	return fill
}

func (f *legacyFill) Guard(ctx context.Context, write func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.owner.mu.RLock()
	defer f.owner.mu.RUnlock()
	if f.invalid || f.closed {
		return storagev1.ErrFillSuperseded
	}
	return write()
}

func (f *legacyFill) Renew(ctx context.Context) (storagev1.LegacyFillLease, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	f.owner.mu.Lock()
	defer f.owner.mu.Unlock()
	return f.owner.beginLocked(f.url), !f.invalid && !f.closed, nil
}

func (f *legacyFill) Close() error {
	f.owner.mu.Lock()
	defer f.owner.mu.Unlock()
	if !f.closed {
		f.closed = true
		delete(f.owner.fills, f)
	}
	return nil
}

func (n *nativeStorage) RegisterRepresentationPurger(name string, purger storagev1.RepresentationPurger) error {
	return n.representations.register(name, purger)
}
func (m *migratorStorage) RegisterRepresentationPurger(name string, purger storagev1.RepresentationPurger) error {
	return m.representations.register(name, purger)
}
func (n *nativeStorage) BeginLegacyFill(ctx context.Context, raw string) (storagev1.LegacyFillLease, error) {
	return n.representations.begin(ctx, raw)
}
func (m *migratorStorage) BeginLegacyFill(ctx context.Context, raw string) (storagev1.LegacyFillLease, error) {
	return m.representations.begin(ctx, raw)
}

var _ storagev1.RepresentationRegistry = (*nativeStorage)(nil)
var _ storagev1.RepresentationRegistry = (*migratorStorage)(nil)
var _ storagev1.LegacyFillCoordinator = (*nativeStorage)(nil)
var _ storagev1.LegacyFillCoordinator = (*migratorStorage)(nil)
