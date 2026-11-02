package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/storage/representation"
)

func (n *nativeStorage) RepresentationManager() *representation.Manager   { return n.manager }
func (m *migratorStorage) RepresentationManager() *representation.Manager { return m.manager }

func initializeRepresentations(config *conf.RepresentationStorage, buckets []storagev1.Bucket, registry *representationRegistry) (*representation.Manager, error) {
	if config == nil {
		// Keeping only legacy reads does not authorize dropping persisted PURGE
		// controls. Operators retain the storage binding when disabling v2 reads.
		for _, bucket := range buckets {
			if controls, ok := bucket.(storagev1.ControlStore); ok {
				md, err := controls.LoadControl(context.Background(), object.BarrierCatalogID())
				if err != nil && !errors.Is(err, storagev1.ErrKeyNotFound) {
					return nil, err
				}
				if md != nil {
					return nil, fmt.Errorf("persisted representation controls require storage.representation binding even when v2 reads are disabled")
				}
			}
		}
		return nil, nil
	}
	interval, budget := config.GCInterval, config.GCBudget
	if interval == 0 {
		interval = time.Minute
	}
	if budget == 0 {
		budget = 64
	}
	var maintenance func(context.Context) error
	if config.LegacyVaryGCBudget < 0 || config.LegacyVaryGCBudget > 128 || config.LegacyVaryGCFileOps < 0 || config.LegacyVaryGCFileOps > 1024 {
		return nil, storagev1.ErrInvalidationBudget
	}
	if config.LegacyVaryGCBudget > 0 {
		fileOps := config.LegacyVaryGCFileOps
		if fileOps == 0 {
			fileOps = 128
		}
		if fileOps < 2 {
			return nil, storagev1.ErrInvalidationBudget
		}
		for _, bucket := range buckets {
			if _, ok := bucket.(storagev1.LegacyVaryReclaimer); !ok {
				return nil, storagev1.ErrControlUnsupported
			}
		}
		maintenance = func(ctx context.Context) error {
			_, err := registry.collectLegacyVary(ctx, buckets, config.LegacyVaryGCBudget, fileOps)
			return err
		}
	}
	durable, err := representationDurability(buckets)
	if err != nil {
		return nil, err
	}
	stateDirectory := config.StateDirectory
	if durable && stateDirectory == "" {
		stateDirectory, err = defaultRepresentationDirectory(context.Background(), buckets)
		if err != nil {
			return nil, err
		}
	} else if !durable {
		// Memory owns a fresh catalog for each instance. It must not silently
		// reuse a disk deployment's state directory while dropping its PURGEs.
		if stateDirectory != "" {
			_, err := os.Stat(filepath.Join(stateDirectory, "representation-topology.json"))
			if err == nil {
				return nil, fmt.Errorf("memory representation cannot reuse durable state directory %q: %w", stateDirectory, representation.ErrTopologyMismatch)
			}
			if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		stateDirectory = ""
	}
	manager, err := representation.NewManager(context.Background(), buckets, representation.ManagerConfig{StateDirectory: stateDirectory, Durable: durable, GCInterval: interval, GCBudget: budget, Maintenance: maintenance})
	if err != nil {
		return nil, err
	}
	if err = registry.register("v2-manager", manager); err != nil {
		manager.Close()
		return nil, err
	}
	return manager, nil
}

const representationDirectoryName = ".tavern-v2"

// A default owner lives beneath a stable bucket root. Reuse an existing owner
// before choosing a path so config ordering cannot relocate its purge history.
// Existing controls without that binding require their original explicit path.
func defaultRepresentationDirectory(ctx context.Context, buckets []storagev1.Bucket) (string, error) {
	var candidates, existing []string
	boundControls := false
	for _, bucket := range buckets {
		if bucket == nil || bucket.Path() == "" {
			return "", representation.ErrInvalidRecord
		}
		root, err := filepath.EvalSymlinks(bucket.Path())
		if err != nil {
			return "", err
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return "", err
		}
		candidate := filepath.Join(root, representationDirectoryName)
		if !slices.Contains(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
		if _, err := os.Stat(filepath.Join(candidate, "representation-topology.json")); err == nil {
			if !slices.Contains(existing, candidate) {
				existing = append(existing, candidate)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if controls, ok := bucket.(storagev1.ControlStore); ok {
			md, err := controls.LoadControl(ctx, object.BarrierCatalogID())
			if err != nil && !errors.Is(err, storagev1.ErrKeyNotFound) {
				return "", err
			}
			if md != nil && md.Barriers != nil && md.Barriers.TopologyFingerprint != "" {
				boundControls = true
			}
		}
	}
	if len(existing) > 1 {
		return "", fmt.Errorf("multiple default representation control directories; configure the original state_directory explicitly: %w", representation.ErrTopologyMismatch)
	}
	if len(existing) == 1 {
		return existing[0], nil
	}
	if boundControls {
		return "", fmt.Errorf("existing v2 controls have no default binding; retain the original state_directory: %w", representation.ErrTopologyMismatch)
	}
	if len(candidates) == 0 {
		return "", representation.ErrInvalidRecord
	}
	slices.Sort(candidates)
	return candidates[0], nil
}

func representationDurability(buckets []storagev1.Bucket) (bool, error) {
	volatile := 0
	for _, bucket := range buckets {
		if memory, ok := bucket.(storagev1.VolatileRepresentationBucket); ok && memory.VolatileRepresentations() {
			volatile++
		}
	}
	if volatile > 0 && volatile != len(buckets) {
		return false, fmt.Errorf("representation cannot mix memory and persistent buckets: %w", storagev1.ErrDurabilityUnsupported)
	}
	return volatile == 0, nil
}
