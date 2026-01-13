package indexdb_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/encoding/cobr"
	"github.com/omalloc/tavern/storage/indexdb"
	"github.com/omalloc/tavern/storage/indexdb/nutsdb"
	"github.com/omalloc/tavern/storage/indexdb/pebble"
)

func BenchmarkPebbleSet(b *testing.B) {
	db := openBenchmarkPebbleDB(b)
	ctx := context.Background()

	id := object.NewID("object-benchmark-set0")
	meta := benchmarkMetadata(id, time.Now().Unix())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.Set(ctx, id.Bytes(), meta); err != nil {
			b.Fatalf("set failed: %v", err)
		}
	}
}

func BenchmarkNutsDBSet(b *testing.B) {
	db := openBenchmarkNutsDB(b)
	ctx := context.Background()

	id := object.NewID("object-benchmark-set0")
	meta := benchmarkMetadata(id, time.Now().Unix())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.Set(ctx, id.Bytes(), meta); err != nil {
			b.Fatalf("set failed: %v", err)
		}
	}
}

func BenchmarkPebbleGet(b *testing.B) {
	const preload = 1024

	db := openBenchmarkPebbleDB(b)
	ctx := context.Background()

	keys := make([][]byte, 0, preload)
	baseTime := time.Now().Unix()
	for i := 0; i < preload; i++ {
		id := object.NewID(fmt.Sprintf("preload-%d", i))
		meta := benchmarkMetadata(id, baseTime)
		if err := db.Set(ctx, id.Bytes(), meta); err != nil {
			b.Fatalf("preload set failed: %v", err)
		}
		keys = append(keys, id.Bytes())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := keys[i%len(keys)]
		if _, err := db.Get(ctx, key); err != nil {
			b.Fatalf("get failed: %v", err)
		}
	}
}

func BenchmarkNutsDBGet(b *testing.B) {
	const preload = 1024

	db := openBenchmarkNutsDB(b)
	ctx := context.Background()

	keys := make([][]byte, 0, preload)
	baseTime := time.Now().Unix()
	for i := 0; i < preload; i++ {
		id := object.NewID(fmt.Sprintf("preload-%d", i))
		meta := benchmarkMetadata(id, baseTime)
		if err := db.Set(ctx, id.Bytes(), meta); err != nil {
			b.Fatalf("preload set failed: %v", err)
		}
		keys = append(keys, id.Bytes())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := keys[i%len(keys)]
		if _, err := db.Get(ctx, key); err != nil {
			b.Fatalf("get failed: %v", err)
		}
	}
}

func openBenchmarkPebbleDB(b *testing.B) storage.IndexDB {
	b.Helper()

	opt := indexdb.NewOption(b.TempDir(),
		indexdb.WithType("pebble"),
		indexdb.WithCodec(&cobr.CborCodec{}),
		indexdb.WithDBConfig(map[string]any{
			"write_sync_mode": false,
		}),
	)
	db, err := pebble.New(opt.DBPath(), opt)
	if err != nil {
		b.Fatalf("failed to open pebble: %v", err)
	}

	b.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func openBenchmarkNutsDB(b *testing.B) storage.IndexDB {
	b.Helper()

	opt := indexdb.NewOption(b.TempDir(), indexdb.WithType("nutsdb"), indexdb.WithCodec(&cobr.CborCodec{}))
	db, err := nutsdb.NewNutsDB(opt.DBPath(), opt)
	if err != nil {
		b.Fatalf("failed to open nutsdb: %v", err)
	}

	b.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func benchmarkMetadata(id *object.ID, now int64) *object.Metadata {
	return &object.Metadata{
		ID:          id,
		BlockSize:   4096,
		Size:        64 << 10,
		RespUnix:    now,
		LastRefUnix: now,
		Code:        200,
	}
}
