package caching

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/storage/representation"
	planning "github.com/omalloc/tavern/transform"
)

type managedPurger struct {
	manager *representation.Manager
	router  *planning.Router
}

var _ storage.RepresentationPurger = (*managedPurger)(nil)

func (p *managedPurger) Purge(ctx context.Context, raw string, control storage.PurgeControl) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return storage.ErrInvalidPurge
	}
	plan, matched, err := p.router.ResolvePurge(request)
	if err != nil {
		return fmt.Errorf("managed purge mapping: %w", storage.ErrInvalidPurge)
	}
	// The base manager is already registered for the original public URI and
	// directories. Add the same durable barrier for a parameter-mapped source,
	// across all namespaces/isolations, before Storage.PURGE may acknowledge.
	if !matched || plan.SourceURL == raw {
		return nil
	}
	return p.manager.Purge(ctx, plan.SourceURL, control)
}
func (p *managedProvider) bindRoutes(router *planning.Router) error {
	registry, ok := p.store.(storage.RepresentationRegistry)
	if !ok {
		return storage.ErrControlUnsupported
	}
	name := fmt.Sprintf("managed-source-map-%x", sha256.Sum256([]byte(p.opt.representation.namespace+"\x00"+p.opt.representation.isolationScope)))
	return registry.RegisterRepresentationPurger(name, &managedPurger{manager: p.manager, router: router})
}

// Keep the exact parameter mapping during a read rollback. Storage still owns
// PURGE and barriers; no worker contact or transform execution is required.
func bindPausedManagedRoutes(store storage.Storage, router *planning.Router) error {
	owner, ok := store.(representation.Provider)
	if !ok || owner.RepresentationManager() == nil {
		return nil
	}
	registry, ok := store.(storage.RepresentationRegistry)
	if !ok {
		return storage.ErrControlUnsupported
	}
	return registry.RegisterRepresentationPurger("managed-paused-source-map", &managedPurger{manager: owner.RepresentationManager(), router: router})
}
