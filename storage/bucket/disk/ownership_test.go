package disk_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/lifecycle"
	"github.com/omalloc/tavern/storage/bucket/disk"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
	"github.com/omalloc/tavern/storage/sharedkv"
)

func TestDiskWriterOwnershipAcrossProcesses(t *testing.T) {
	if path := os.Getenv("TAVERN_OWNER_PATH"); path != "" {
		b, err := disk.New(&storage.BucketConfig{Path: path, DBPath: os.Getenv("TAVERN_OWNER_DB"), DBType: "pebble", Driver: "native"}, sharedkv.NewEmpty())
		if os.Getenv("TAVERN_OWNER_EXPECT") == "denied" {
			if err == nil {
				b.Close()
				t.Fatal("second process acquired an active writer directory")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		id := object.NewID("https://example.com/ownership")
		w, _, err := b.WriteChunkFile(t.Context(), id, 0)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, "new!")
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		if err = b.Store(t.Context(), &object.Metadata{ID: id, Headers: make(http.Header)}); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := t.TempDir()
	dbpath := filepath.Join(path, ".index")
	b, err := disk.New(&storage.BucketConfig{Path: path, DBPath: dbpath, DBType: "pebble", Driver: "native"}, sharedkv.NewEmpty())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	child := func(path, dbpath, expect string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestDiskWriterOwnershipAcrossProcesses$", "-test.count=1")
		cmd.Env = append(os.Environ(), "TAVERN_OWNER_PATH="+path, "TAVERN_OWNER_DB="+dbpath, "TAVERN_OWNER_EXPECT="+expect)
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ownership child: %v\n%s", err, data)
		}
	}
	child(path, filepath.Join(path, ".other-index"), "denied")
	child(t.TempDir(), dbpath, "denied")
	id := object.NewID("https://example.com/ownership")
	w, _, err := b.WriteChunkFile(t.Context(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "old!")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = b.(storage.Quiescer).Quiesce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("open writer was treated as stopped", err)
	}
	child(path, dbpath, "denied")
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = b.(storage.Quiescer).Quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	child(path, dbpath, "allowed")
	stale := &object.Metadata{ID: id, Headers: make(http.Header), Size: 4, BlockSize: 4}
	stale.Chunks.Set(0)
	if err = b.DiscardWithMetadata(t.Context(), stale); !errors.Is(err, lifecycle.ErrDraining) {
		t.Fatal("late delete was not fenced", err)
	}
	if err = b.Store(t.Context(), stale); !errors.Is(err, lifecycle.ErrDraining) {
		t.Fatal("late metadata write was not fenced", err)
	}
	data, err := os.ReadFile(id.WPathSlice(path, 0))
	if err != nil || string(data) != "new!" {
		t.Fatal("old writer corrupted the new generation", fmt.Sprintf("%q", data), err)
	}
}
