package storage_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/storage"
	_ "github.com/omalloc/tavern/storage/bucket/disk"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
)

func TestSelect(t *testing.T) {
	dir := t.TempDir()

	s, err := storage.New(&conf.Storage{
		DBType:          "pebble",
		Driver:          "native",
		AsyncLoad:       true,
		EvictionPolicy:  "lru",
		SelectionPolicy: "hashring",
		Buckets: []*conf.Bucket{
			{Path: filepath.Join(dir, "/cache1"), Type: "normal"},
			{Path: filepath.Join(dir, "/cache2"), Type: "normal"},
		},
	}, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}

	cacheKey := object.NewID("http://www.example.com/path/to/1K.bin")

	bucket := s.Select(context.Background(), cacheKey)

	if bucket == nil {
		t.Fatal("no bucket selected")
	}

	_ = bucket.Store(context.Background(), &object.Metadata{
		ID:      cacheKey,
		Chunks:  nil,
		Parts:   nil,
		Size:    1024,
		Code:    200,
		Headers: make(http.Header),
		Flags:   object.FlagCache,
	})

	md, err := bucket.Lookup(context.Background(), cacheKey)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("object metadata: %+v", md)
}
