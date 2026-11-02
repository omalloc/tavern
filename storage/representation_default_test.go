package storage_test

import (
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

func defaultRepresentationConfig(paths ...string) *conf.Storage {
	cfg := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{}}
	for _, path := range paths {
		cfg.Buckets = append(cfg.Buckets, &conf.Bucket{Path: path, MaxObjectLimit: 100})
	}
	return cfg
}

func openDefaultRepresentationStorage(t *testing.T, cfg *conf.Storage) storagev1.Storage {
	t.Helper()
	store, err := storage.New(cfg, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestDefaultRepresentationDirectoryReopensAndKeepsPurge(t *testing.T) {
	for _, migration := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "migrator"}[migration], func(t *testing.T) {
			base := t.TempDir()
			a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
			cfg := defaultRepresentationConfig(b, a)
			cfg.Migration = &conf.Migration{Enabled: migration}
			store := openDefaultRepresentationStorage(t, cfg)
			key, _ := object.NewResourceKey("https://example.com/item", "v2", "origin")
			repo, err := store.(representation.Provider).RepresentationManager().Repository(key)
			if err != nil || !repo.Durable() {
				t.Fatal("default directory disabled durability", err)
			}
			initial, err := repo.Capture(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PURGE(key.CanonicalURL(), storagev1.PurgeControl{Hard: true}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(a, ".tavern-v2", "representation-topology.json")
			if _, err := os.Stat(manifest); err != nil {
				t.Fatal("default binding not beneath canonical first bucket", err)
			}
			cfg.Buckets[0], cfg.Buckets[1] = cfg.Buckets[1], cfg.Buckets[0]
			store = openDefaultRepresentationStorage(t, cfg)
			repo, err = store.(representation.Provider).RepresentationManager().Repository(key)
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := repo.Capture(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			if reopened.Resource().Resource.Incarnation != initial.Resource().Resource.Incarnation || reopened.Resource().Resource.PurgeEpoch == 0 {
				t.Fatal("reordered buckets relocated or lost control history")
			}
			if _, err := os.Stat(filepath.Join(b, ".tavern-v2")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("reordered config created another control owner", err)
			}
			store.Close()
		})
	}
}

func TestDefaultRepresentationDirectoryReusesExistingNonFirstBucket(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	cfg := defaultRepresentationConfig(a, b)
	cfg.Representation.StateDirectory = filepath.Join(b, ".tavern-v2")
	store := openDefaultRepresentationStorage(t, cfg)
	store.Close()
	cfg.Representation.StateDirectory = ""
	store = openDefaultRepresentationStorage(t, cfg)
	store.Close()
	if _, err := os.Stat(filepath.Join(a, ".tavern-v2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("existing default owner was relocated", err)
	}
}

func TestDefaultRepresentationDirectoryPreservesExplicitOverride(t *testing.T) {
	root, explicit := t.TempDir(), t.TempDir()
	cfg := defaultRepresentationConfig(root)
	cfg.Representation.StateDirectory = explicit
	store := openDefaultRepresentationStorage(t, cfg)
	store.Close()
	if _, err := os.Stat(filepath.Join(explicit, "representation-topology.json")); err != nil {
		t.Fatal(err)
	}
	cfg.Representation.StateDirectory = ""
	unsafe, err := storage.New(cfg, log.DefaultLogger)
	if unsafe != nil {
		unsafe.Close()
	}
	if !errors.Is(err, representation.ErrTopologyMismatch) || !strings.Contains(err.Error(), "original state_directory") {
		t.Fatal("omitting an external binding silently relocated existing controls", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".tavern-v2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("implicit relocation created a new owner", err)
	}
	cfg.Representation.StateDirectory = explicit
	store = openDefaultRepresentationStorage(t, cfg)
	store.Close()
}

func TestDefaultRepresentationDirectoryRejectsMissingBinding(t *testing.T) {
	root := t.TempDir()
	cfg := defaultRepresentationConfig(root)
	store := openDefaultRepresentationStorage(t, cfg)
	store.Close()
	manifest := filepath.Join(root, ".tavern-v2", "representation-topology.json")
	backup := manifest + ".backup"
	if err := os.Rename(manifest, backup); err != nil {
		t.Fatal(err)
	}
	unsafe, err := storage.New(cfg, log.DefaultLogger)
	if unsafe != nil {
		unsafe.Close()
	}
	if !errors.Is(err, representation.ErrTopologyMismatch) {
		t.Fatal("missing default binding was silently rebuilt", err)
	}
	if _, err := os.Stat(manifest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing binding was recreated", err)
	}
	if err := os.Rename(backup, manifest); err != nil {
		t.Fatal(err)
	}
	store = openDefaultRepresentationStorage(t, cfg)
	store.Close()
}

func TestDefaultRepresentationDirectoryRejectsTopologyChangeAndAmbiguity(t *testing.T) {
	base := t.TempDir()
	a, b, c := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "0-new")
	cfg := defaultRepresentationConfig(a, b)
	store := openDefaultRepresentationStorage(t, cfg)
	store.Close()
	cfg.Buckets = append(cfg.Buckets, &conf.Bucket{Path: c, MaxObjectLimit: 100})
	unsafe, err := storage.New(cfg, log.DefaultLogger)
	if unsafe != nil {
		unsafe.Close()
	}
	if !errors.Is(err, representation.ErrTopologyMismatch) {
		t.Fatal("new bucket silently replaced the default control owner", err)
	}
	if _, err := os.Stat(filepath.Join(c, ".tavern-v2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("changed topology created another control binding", err)
	}
	cfg.Buckets = cfg.Buckets[:2]
	data, err := os.ReadFile(filepath.Join(a, ".tavern-v2", "representation-topology.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(b, ".tavern-v2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, ".tavern-v2", "representation-topology.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	unsafe, err = storage.New(cfg, log.DefaultLogger)
	if unsafe != nil {
		unsafe.Close()
	}
	if !errors.Is(err, representation.ErrTopologyMismatch) || !strings.Contains(err.Error(), "multiple default") {
		t.Fatal("ambiguous default bindings were silently selected", err)
	}
	cfg.Representation.StateDirectory = filepath.Join(a, ".tavern-v2")
	store = openDefaultRepresentationStorage(t, cfg)
	store.Close()
}
