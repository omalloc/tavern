package storage

import (
	"context"
	"errors"
	"fmt"
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
					return nil, fmt.Errorf("persisted representation controls require storage.representation.state_directory even when v2 reads are disabled")
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
	manager, err := representation.NewManager(context.Background(), buckets, representation.ManagerConfig{StateDirectory: config.StateDirectory, Durable: true, GCInterval: interval, GCBudget: budget, Maintenance: maintenance})
	if err != nil {
		return nil, err
	}
	if err = registry.register("v2-manager", manager); err != nil {
		manager.Close()
		return nil, err
	}
	return manager, nil
}
