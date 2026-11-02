package disk_test

import (
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

func TestStartupUnindexedCleanupPreservesOwnedAndUnknownFiles(t *testing.T) {
	base := t.TempDir()
	bucket := newTestBucket(t, base)
	t.Cleanup(func() { bucket.Close() })
	write := func(id *object.ID, index uint32) string {
		t.Helper()
		writer, path, err := bucket.WriteChunkFile(t.Context(), id, index)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, "chunk"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	kept := object.NewID("https://example.com/legacy")
	path := write(kept, 0)
	md := &object.Metadata{ID: kept, Code: 200, Size: 5, BlockSize: 64, Headers: make(http.Header)}
	md.Chunks.Set(0)
	if err := bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	unowned := write(kept, 1)
	orphan := write(object.NewID("https://example.com/no-index"), 0)
	temporary, unknown := path+".tmp20261102111416", path+".unknown"
	for _, name := range []string{temporary, unknown} {
		if err := os.WriteFile(name, []byte("untouched"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := bucket.(storage.UnindexedChunkRecoverer).RecoverUnindexedChunks(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, unknown} {
		if _, err := os.Stat(name); err != nil {
			t.Fatal("recovery deleted an owned/unknown file", name, err)
		}
	}
	for _, name := range []string{unowned, orphan, temporary} {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("recovery retained an unowned cache file", name, err)
		}
	}
}
