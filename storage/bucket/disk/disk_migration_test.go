package disk_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/disk"
	"github.com/omalloc/tavern/storage/sharedkv"
)

// MockMigration
type MockMigration struct {
	mock.Mock
}

func (m *MockMigration) Promote(ctx context.Context, id *object.ID, src storage.Bucket) error {
	args := m.Called(ctx, id, src)
	return args.Error(0)
}

func (m *MockMigration) Demote(ctx context.Context, id *object.ID, src storage.Bucket) error {
	args := m.Called(ctx, id, src)
	return args.Error(0)
}

func TestMigration_Promote(t *testing.T) {
	basepath := t.TempDir()
	shared := sharedkv.NewMemSharedKV()

	cfg := &storage.BucketConfig{
		Path:   basepath,
		DBPath: filepath.Join(basepath, ".indexdb"),
		DBType: "pebble",
		Driver: "native",
		Type:   storage.TypeWarm,
		Migration: &storage.MigrationConfig{
			Enabled: true,
			Promote: storage.PromoteConfig{
				MinHits: 2, // Changed to 2 for easier testing
				Window:  time.Minute,
			},
		},
		MaxObjectLimit: 100,
	}

	b, err := disk.New(cfg, shared)
	assert.NoError(t, err)
	defer b.Close()

	mockMig := new(MockMigration)
	_ = b.SetMigration(mockMig)

	id := object.NewID("http://example.com/obj1")

	// Store object
	err = b.Store(context.Background(), &object.Metadata{
		ID:          id,
		Code:        200,
		Size:        100,
		LastRefUnix: time.Now().Unix(),
		Refs:        1,
	})
	assert.NoError(t, err)

	mockMig.On("Promote", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Hit 1 -> Total Hits = 1 (Store doesn't count as hit in HeavyKeeper usually, but let's check implementation)
	// In disk.go: Store call cache.Set. touch call cache.Set.
	// touch logic: hkPromote.Add
	// Lookup calls touch.

	// Lookup 1
	b.Touch(context.Background(), id)
	// Lookup 2
	b.Touch(context.Background(), id)

	// Wait for async promote
	time.Sleep(time.Second * 1)

	mockMig.AssertCalled(t, "Promote", mock.Anything, mock.Anything, mock.Anything)
}

func TestMigration_Demote(t *testing.T) {
	basepath := t.TempDir()
	shared := sharedkv.NewMemSharedKV()

	cfg := &storage.BucketConfig{
		Path:   basepath,
		DBPath: filepath.Join(basepath, ".indexdb"),
		DBType: "pebble",
		Driver: "native",
		Type:   storage.TypeWarm,
		Migration: &storage.MigrationConfig{
			Enabled: true,
			Demote: storage.DemoteConfig{
				MinHits:   10,
				Window:    time.Minute,
				Occupancy: 40, // Trigger at 40%
			},
		},
		MaxObjectLimit: 10,
	}

	b, err := disk.New(cfg, shared)
	assert.NoError(t, err)
	defer b.Close()

	mockMig := new(MockMigration)
	_ = b.SetMigration(mockMig)

	mockMig.On("Demote", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// (11 items) 1 item demote
	for i := 0; i < 11; i++ {
		id := object.NewID(fmt.Sprintf("http://example.com/obj%d", i))
		_ = b.Store(context.Background(), &object.Metadata{
			ID:   id,
			Code: 200,
		})
	}

	// Wait for demote ticker
	time.Sleep(time.Second)

	// Verify Demote was called
	mockMig.AssertCalled(t, "Demote", mock.Anything, mock.Anything, mock.Anything)
}
