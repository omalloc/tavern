package caching

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/storage"
	"github.com/omalloc/tavern/storage/representation"
)

func registeredOriginFixture(t *testing.T, handler http.HandlerFunc) (*originFixture, storagev1.Storage) {
	t.Helper()
	f := newOriginFixture(t, handler)
	base := t.TempDir()
	store, err := storage.New(&conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Buckets: []*conf.Bucket{{Path: filepath.Join(base, "cache"), Type: storagev1.TypeWarm, MaxObjectLimit: 100}}}, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	bucket := store.Buckets()[0]
	repo, err := representation.NewIndexRepository(bucket.(storagev1.ControlStore), bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return bucket.Lookup(ctx, id)
	}, representation.WithClock(func() time.Time { return time.Unix(f.clock.Load(), 0) }), representation.WithDurability(bucket))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.(storagev1.RepresentationRegistry).RegisterRepresentationPurger("http-v2", repo); err != nil {
		t.Fatal(err)
	}
	f.bucket, f.repo = bucket, repo
	f.opt.representation.repository = repo
	f.rt = representationTestTransport(store, f.opt)
	return f, store
}

func TestRepresentationPurgeRejectsLate200And304(t *testing.T) {
	for _, status := range []int{200, 304} {
		for _, hard := range []bool{false, true} {
			t.Run(map[int]string{200: "200", 304: "304"}[status]+map[bool]string{false: "/soft", true: "/hard"}[hard], func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				var calls atomic.Int32
				f, store := registeredOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
					switch calls.Add(1) {
					case 1:
						originImage(w, "old", "Accept", `"old"`)
					case 2:
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
						if status == 304 {
							w.Header().Set("Age", "0")
							w.WriteHeader(304)
						} else {
							originImage(w, "late", "Accept", `"late"`)
						}
					default:
						originImage(w, "current", "Accept", `"current"`)
					}
				})
				header := http.Header{"Accept": {"image/jpeg"}}
				f.get(t, header)
				f.clock.Add(601)
				type result struct {
					body string
					err  error
				}
				done := make(chan result, 1)
				go func() {
					_, body, err := readVaryResponse(f.rt, varyRequest(t, f.target, header))
					done <- result{body, err}
				}()
				select {
				case <-entered:
				case <-time.After(10 * time.Second):
					t.Fatal("validation did not enter origin")
				}
				if err := store.PURGE(f.target, storagev1.PurgeControl{Hard: hard, MarkExpired: !hard}); err != nil {
					t.Fatal(err)
				}
				unblock()
				completed := <-done
				if completed.err != nil {
					t.Fatal(completed.err)
				}
				selected, err := f.repo.Lookup(t.Context(), f.key, header)
				if hard && !errors.Is(err, storagev1.ErrKeyNotFound) || !hard && (err != nil || selected.Fresh) {
					t.Fatal("late response crossed acknowledged PURGE", selected, err)
				}
				if _, body := f.get(t, header); body != "current" {
					t.Fatal("purged representation became fresh again", body)
				}
				f.get(t, header)
				if calls.Load() != 3 {
					t.Fatal("post-purge fill did not commit independently", calls.Load())
				}
			})
		}
	}
}
