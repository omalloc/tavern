package caching

import (
	"context"
	"fmt"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
	"github.com/omalloc/tavern/storage/representation"
)

// Read enablement is independent of storage control ownership. Disabling reads
// leaves the storage manager registered for PURGE and bounded orphan cleanup.
type representationOption struct {
	Enabled          bool   `json:"enabled" yaml:"enabled"`
	Namespace        string `json:"namespace" yaml:"namespace"`
	IsolationScope   string `json:"isolation_scope" yaml:"isolation_scope"`
	MaxVaryFields    int    `json:"max_vary_fields" yaml:"max_vary_fields"`
	MaxSelectorBytes int    `json:"max_selector_bytes" yaml:"max_selector_bytes"`
}

func (o *cachingOption) bindRepresentations(store storage.Storage) error {
	config := o.Representation
	if config == nil || !config.Enabled {
		return nil
	}
	if config.Namespace == "" {
		config.Namespace = "http-v2"
	}
	if config.IsolationScope == "" {
		config.IsolationScope = "origin"
	}
	if config.MaxVaryFields != 0 && config.MaxVaryFields != varycontrol.MaxFields {
		return fmt.Errorf("representation.max_vary_fields currently must be 32")
	}
	if config.MaxSelectorBytes != 0 && config.MaxSelectorBytes != varycontrol.MaxSnapshotBytes {
		return fmt.Errorf("representation.max_selector_bytes currently must be %d", varycontrol.MaxSnapshotBytes)
	}
	if o.VaryLimit <= 0 || o.SliceSize == 0 {
		return fmt.Errorf("representation requires positive vary_limit and slice_size")
	}
	provider, ok := store.(representation.Provider)
	if !ok || provider.RepresentationManager() == nil {
		return fmt.Errorf("representation.enabled requires storage.representation binding")
	}
	if !o.IncludeQueryInCacheKey {
		if err := provider.RepresentationManager().BindQuerylessNamespace(context.Background(), config.Namespace); err != nil {
			return fmt.Errorf("bind representation query policy: %w", err)
		}
	}
	o.representation = &representationLookup{manager: provider.RepresentationManager(), namespace: config.Namespace, isolationScope: config.IsolationScope, writes: true}
	return nil
}
