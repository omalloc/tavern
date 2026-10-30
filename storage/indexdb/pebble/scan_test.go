package pebble_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/indexdb"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
)

func TestScanIsBoundedAndCursorSurvivesDeletion(t *testing.T) {
	db, err := indexdb.Create("pebble", indexdb.NewOption("memory", indexdb.WithType("pebble")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		id := object.NewID("https://example.com/" + name)
		if err = db.Set(t.Context(), id.Bytes(), &object.Metadata{ID: id, Headers: make(http.Header)}); err != nil {
			t.Fatal(err)
		}
	}
	scanner := db.(storage.IndexScanner)
	seen := make(map[object.IDHash]bool)
	var cursor []byte
	for {
		records, next, err := scanner.Scan(t.Context(), cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) > 2 {
			t.Fatal("unbounded page")
		}
		for _, md := range records {
			if seen[md.ID.Hash()] {
				t.Fatal("duplicate record")
			}
			seen[md.ID.Hash()] = true
		}
		if next == nil {
			break
		}
		if len(cursor) > 0 && bytes.Compare(next, cursor) <= 0 {
			t.Fatal("cursor did not advance")
		}
		// Delete the cursor itself between pages: SeekGE must not skip its successor.
		if err = db.Delete(t.Context(), next); err != nil {
			t.Fatal(err)
		}
		cursor = bytes.Clone(next)
	}
	if len(seen) != 5 {
		t.Fatal("skipped records", len(seen))
	}
	if _, _, err = scanner.Scan(t.Context(), nil, 0); err == nil {
		t.Fatal("accepted unbounded limit")
	}
}
