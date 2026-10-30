package main

import (
	"context"
	"fmt"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
)

// Upgrade can spawn the next writer only after accepted work and open data
// handles have stopped and the old DB/ownership locks have closed successfully.
func handoffStorage(ctx context.Context, store storagev1.Storage, upgrade func() error) error {
	coordinator, ok := store.(storagev1.TaskCoordinator)
	if !ok {
		return fmt.Errorf("storage does not support a safe writer handoff")
	}
	if err := coordinator.Drain(ctx); err != nil {
		return fmt.Errorf("drain storage before upgrade: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close storage before upgrade: %w", err)
	}
	return upgrade()
}
