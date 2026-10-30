package caching

import (
	"io"
	"net/http"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
)

// Fixed 8x8 JPEG source and actual Go PNG output. The memory/Pebble-vfs index
// isolates pipeline overhead; these are not production disk or remote RPC rates.
func BenchmarkManagedRepresentation(b *testing.B) {
	for _, mode := range []string{"hot_product", "rebuild_product_with_maintenance"} {
		b.Run(mode, func(b *testing.B) {
			previous := log.GetLogger()
			log.SetLogger(log.NewFilter(previous, log.FilterLevel(log.LevelError)))
			b.Cleanup(func() { log.SetLogger(previous) })
			f := newManagedFixture(b)
			req, _ := http.NewRequestWithContext(b.Context(), "GET", "https://example.com/images/a.jpg?format=png", nil)
			req.Header.Set("Accept", "image/png")
			req.Header.Set("X-Tenant", "tenant-a")
			plan, matched, err := f.router.Resolve(req)
			if err != nil || !matched {
				b.Fatal(err)
			}
			read := func() bool {
				result, err := f.provider.resolve(req, plan)
				if err != nil {
					b.Fatal(err)
				}
				reader, err := result.Open(b.Context())
				if err != nil {
					result.Close()
					b.Fatal(err)
				}
				_, err = io.Copy(io.Discard, reader)
				reader.Close()
				result.Close()
				if err != nil {
					b.Fatal(err)
				}
				return result.Cached
			}
			read()
			source, err := f.provider.source(req, plan)
			if err != nil {
				b.Fatal(err)
			}
			source.close()
			bucket := f.store.bucket.(*sourceReadBucket)
			reads, writes := bucket.controlReads.Load(), bucket.controlWrites.Load()
			hits := 0
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "rebuild_product_with_maintenance" {
					root, err := source.repo.Load(b.Context(), source.key)
					if err != nil {
						b.Fatal(err)
					}
					for _, ref := range root.Resource.Variants {
						if ref.SelectionKind == object.SelectionManaged {
							if _, err := source.repo.Retire(b.Context(), source.key, ref); err != nil {
								b.Fatal(err)
							}
						}
					}
				}
				if read() {
					hits++
				}
				if mode == "rebuild_product_with_maintenance" {
					if _, err := f.store.manager.CollectUnreachable(b.Context(), 64); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(f.origins.Load()), "origin_calls")
			b.ReportMetric(float64(f.backend.calls.Load()), "transform_calls")
			b.ReportMetric(float64(hits)/float64(b.N), "product_hit_ratio")
			b.ReportMetric(float64(bucket.controlReads.Load()-reads)/float64(b.N), "control_reads/op")
			b.ReportMetric(float64(bucket.controlWrites.Load()-writes)/float64(b.N), "control_writes/op")
		})
	}
}
