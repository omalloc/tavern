package storage_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/storage"
	"github.com/omalloc/tavern/storage/representation"
)

func TestMemoryRepresentationStartupAndFreshIncarnation(t *testing.T) {
	for _, migration := range []bool{false, true} {
		for _, configuredDirectory := range []bool{false, true} {
			t.Run(map[bool]string{false: "native", true: "migrator"}[migration]+map[bool]string{false: "/no-state", true: "/unused-state"}[configuredDirectory], func(t *testing.T) {
				state := filepath.Join(t.TempDir(), "control")
				representationConfig := &conf.RepresentationStorage{LegacyVaryGCBudget: 1}
				if configuredDirectory {
					representationConfig.StateDirectory = state
				}
				cfg := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{Enabled: true}, Migration: &conf.Migration{Enabled: migration}, Representation: representationConfig, Buckets: []*conf.Bucket{{Path: "inmemory", Driver: "memory", Type: storagev1.TypeWarm, MaxObjectLimit: 100}}}
				key, _ := object.NewResourceKey("https://example.com/images/a", "memory-v2", "origin")
				open := func() (storagev1.Storage, *representation.IndexRepository) {
					t.Helper()
					store, err := storage.New(cfg, log.DefaultLogger)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { store.Close() })
					manager := store.(representation.Provider).RepresentationManager()
					if manager == nil {
						t.Fatal("memory storage omitted v2 owner")
					}
					repo, err := manager.Repository(key)
					if err != nil || repo.Durable() {
						t.Fatal("memory claimed persistent durability", err)
					}
					return store, repo
				}
				store, repo := open()
				first, err := repo.Capture(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.PURGE(key.CanonicalURL(), storagev1.PurgeControl{Hard: true}); err != nil {
					t.Fatal(err)
				}
				purged, err := repo.Capture(t.Context(), key)
				if err != nil || purged.Resource().Resource.PurgeEpoch == 0 {
					t.Fatal("in-process purge did not fence memory fills", err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, repo = open()
				if _, err := repo.Load(t.Context(), key); !errors.Is(err, storagev1.ErrKeyNotFound) {
					t.Fatal("reopen retained memory metadata", err)
				}
				fresh, err := repo.Capture(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				root := fresh.Resource().Resource
				if root.Incarnation == first.Resource().Resource.Incarnation || root.PurgeEpoch != 0 || root.RevalidateEpoch != 0 {
					t.Fatal("memory inherited a previous instance's control history")
				}
				store.Close()
				if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("memory created persistent control state", err)
				}
			})
		}
	}
}

func TestMemoryRepresentationRejectsMixedStorage(t *testing.T) {
	store, err := storage.New(&conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{}, Buckets: []*conf.Bucket{{Path: "inmemory", Driver: "memory", MaxObjectLimit: 100}, {Path: t.TempDir(), MaxObjectLimit: 100}}}, log.DefaultLogger)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, storagev1.ErrDurabilityUnsupported) || !strings.Contains(err.Error(), "mix memory") {
		t.Fatal("mixed buckets silently downgraded disk controls", err)
	}
}

func TestMemoryRepresentationDoesNotReuseDurableState(t *testing.T) {
	cfg := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()}, Buckets: []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}
	store, err := storage.New(cfg, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(cfg.Representation.StateDirectory, "representation-topology.json")
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Buckets[0].Driver = "memory"
	store, err = storage.New(cfg, log.DefaultLogger)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, representation.ErrTopologyMismatch) {
		t.Fatal("memory reused a durable deployment's control binding", err)
	}
	after, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("memory rewrote disk history", err)
	}
	cfg.Buckets[0].Driver = "native"
	store, err = storage.New(cfg, log.DefaultLogger)
	if err != nil {
		t.Fatal("original disk owner could not reopen", err)
	}
	store.Close()
}
