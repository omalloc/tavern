package caching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"github.com/omalloc/tavern/storage/representation"
	planning "github.com/omalloc/tavern/transform"
)

type managedLimits struct {
	Concurrency, QueueSize          int
	InputBytes, OutputBytes, Pixels int64
	Timeout                         time.Duration
	Directory                       string
}
type managedProvider struct {
	store             storage.Storage
	origin            http.RoundTripper
	opt               cachingOption
	backend           transformv1.Transformer
	caps              transformv1.BackendCapabilities
	inspector         planning.Inspector
	limits            managedLimits
	manager           *representation.Manager
	sources, products keyedGate
	budget            *planning.WorkBudget
}

type managedResult struct {
	Metadata      *object.Metadata
	Info          transformv1.SourceInfo
	Cached        bool
	SourceHeaders http.Header
	open          func(context.Context) (io.ReadCloser, error)
	finish        func() error
	once          sync.Once
	closeErr      error
}

func (r *managedResult) Open(ctx context.Context) (io.ReadCloser, error) { return r.open(ctx) }
func (r *managedResult) Close() error {
	r.once.Do(func() {
		if r.finish != nil {
			r.closeErr = r.finish()
		}
	})
	return r.closeErr
}

type managedSource struct {
	repo     *representation.IndexRepository
	key      object.ResourceKey
	request  *http.Request
	selected *representation.LookupResult
	blob     *planning.FileBlob
	headers  http.Header
	info     transformv1.SourceInfo
}

func (s *managedSource) close() {
	if s.blob != nil {
		s.blob.Close()
	}
}

func newManagedProvider(store storage.Storage, origin http.RoundTripper, opt cachingOption, backend transformv1.Transformer, inspector planning.Inspector, limits managedLimits) (*managedProvider, error) {
	if store == nil || origin == nil || backend == nil || inspector == nil || limits.InputBytes <= 0 || limits.OutputBytes <= 0 || limits.Pixels <= 0 || limits.Timeout <= 0 || opt.SliceSize == 0 || opt.VaryLimit <= 0 || opt.representation == nil {
		return nil, planning.ErrInvalidParameter
	}
	owner, ok := store.(representation.Provider)
	if !ok || owner.RepresentationManager() == nil {
		return nil, storage.ErrControlUnsupported
	}
	caps := backend.Capabilities()
	if err := planning.ValidateCapabilities(caps); err != nil {
		return nil, err
	}
	// Backend-owned slice mutation cannot change an accepted task's identity.
	encoded, _ := json.Marshal(caps)
	caps = transformv1.BackendCapabilities{}
	json.Unmarshal(encoded, &caps)
	opt.FuzzyRefresh = false
	opt.IncludeQueryInCacheKey = true
	opt.representation = &representationLookup{manager: owner.RepresentationManager(), namespace: opt.representation.namespace + "/managed-source-v1", isolationScope: opt.representation.isolationScope, writes: true}
	if limits.Concurrency == 0 {
		limits.Concurrency = 2
	}
	if limits.QueueSize == 0 {
		limits.QueueSize = 32
	}
	budget, err := planning.NewWorkBudget(limits.Concurrency, limits.QueueSize)
	if err != nil {
		return nil, err
	}
	return &managedProvider{store: store, origin: origin, opt: opt, backend: backend, caps: caps, inspector: inspector, limits: limits, manager: owner.RepresentationManager(), budget: budget}, nil
}

// resolve owns an accepted, bounded task independently of the requesting
// reader. Cancellation stops waiting; completed unclaimed outputs are closed.
func (p *managedProvider) resolve(req *http.Request, plan planning.Plan) (*managedResult, error) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead || req.Body != nil && req.Body != http.NoBody {
		return nil, planning.ErrInvalidParameter
	}
	plan = plan.Clone()
	ctx, finish, err := beginCacheTask(p.store, context.WithoutCancel(req.Context()), p.limits.Timeout)
	if err != nil {
		return nil, err
	}
	type outcome struct {
		result *managedResult
		err    error
	}
	completed := make(chan outcome)
	go func() {
		defer finish()
		release, err := p.budget.Acquire(ctx)
		var result *managedResult
		if err == nil {
			result, err = p.resolveTask(req.Clone(ctx), plan)
			if result != nil && result.Metadata.ID == nil {
				previous := result.finish
				result.finish = func() error {
					defer release()
					if previous != nil {
						return previous()
					}
					return nil
				}
			} else {
				release()
			}
		}
		select {
		case completed <- outcome{result, err}:
		case <-req.Context().Done():
			if result != nil {
				result.Close()
			}
		}
	}()
	select {
	case result := <-completed:
		return result.result, result.err
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}
func (p *managedProvider) resolveTask(req *http.Request, plan planning.Plan) (*managedResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		source, err := p.source(req, plan)
		if errors.Is(err, representation.ErrSuperseded) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("managed source: %w", err)
		}
		result, err := p.resolveSource(req, plan, source)
		source.close()
		if errors.Is(err, representation.ErrSuperseded) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("managed product: %w", err)
		}
		return result, nil
	}
	return nil, representation.ErrSuperseded
}
func (p *managedProvider) resolveSource(req *http.Request, plan planning.Plan, source *managedSource) (*managedResult, error) {
	accept, err := planning.ParseAccept(plan.AcceptHeader)
	if err != nil {
		return nil, err
	}
	decision, err := planning.Resolve(accept, source.info, plan.Policy, plan.Options, p.caps)
	if err != nil {
		return nil, err
	}
	if decision.PreserveSource {
		return p.sourceResult(req.Context(), source)
	}
	if source.selected == nil {
		return p.convert(req.Context(), source, *decision.Spec, nil, nil)
	}
	ref, err := representation.NewManagedSelector(source.selected, decision.Spec.Fingerprint())
	if err != nil {
		return nil, fmt.Errorf("select source dependency: %w", err)
	}
	state, err := source.repo.Capture(req.Context(), source.key)
	if err != nil {
		return nil, fmt.Errorf("capture product: %w", err)
	}
	// The same spec under a different source/epoch never joins an old job.
	root := state.Resource().Resource
	flight := fmt.Sprintf("%s/%s/%d/%d/%d/%s", source.key.ResourceID(), root.Incarnation, root.PurgeEpoch, root.RevalidateEpoch, root.RoutingEpoch, ref.SelectorFingerprint)
	unlock, err := p.products.lock(req.Context(), flight)
	if err != nil {
		return nil, err
	}
	defer unlock()
	selected, err := source.repo.LookupDerivedRead(req.Context(), source.key, ref.SelectedSourceID, ref.SelectedSourceVersion, ref.TransformFingerprint)
	if err == nil {
		return p.cachedResult(source, selected, true)
	}
	if !errors.Is(err, storage.ErrKeyNotFound) {
		return nil, fmt.Errorf("lookup product: %w", err)
	}
	lease, err := source.repo.ReserveFor(req.Context(), state, ref, p.opt.VaryLimit)
	if err != nil {
		return nil, fmt.Errorf("reserve product: %w", err)
	}
	defer source.repo.Release(lease)
	return p.convert(req.Context(), source, *decision.Spec, state, lease)
}
