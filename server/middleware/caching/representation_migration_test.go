package caching

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/disk"
	"github.com/omalloc/tavern/storage/representation"
	"github.com/omalloc/tavern/storage/sharedkv"
)

type representationSetStorage struct {
	storage.Storage
	buckets []storage.Bucket
}

func (s *representationSetStorage) Select(context.Context, *object.ID) storage.Bucket {
	return s.buckets[0]
}
func (s *representationSetStorage) Buckets() []storage.Bucket { return s.buckets }

func TestRepresentation304ReadsPinnedLocationAcrossMigration(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			originImage(w, "original-image", "Accept", `"v1"`)
			return
		}
		if r.Header.Get("If-None-Match") != `"v1"` {
			t.Error("missing conditional validator")
		}
		close(entered)
		<-release
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(304)
	})
	f.clock.Store(time.Now().Unix())
	path := t.TempDir()
	other, err := disk.New(&storage.BucketConfig{Path: path, DBPath: filepath.Join(path, ".index"), DBType: "pebble", Driver: "native", MaxObjectLimit: 100}, sharedkv.NewEmpty())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	buckets := []storage.Bucket{f.bucket, other}
	manager, err := representation.NewManager(t.Context(), buckets, representation.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	f.repo, err = manager.Repository(f.key)
	if err != nil {
		t.Fatal(err)
	}
	f.opt.representation.repository = nil
	f.opt.representation.manager = manager
	f.rt = representationTestTransport(&representationSetStorage{buckets: buckets}, f.opt)
	header := http.Header{"Accept": {"image/jpeg"}}
	if _, body := f.get(t, header); body != "original-image" {
		t.Fatal(body)
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil {
		t.Fatal(err)
	}
	var source, target storage.Bucket
	for _, b := range buckets {
		if b.ID() == selected.Resource.Resource.ActiveLocation {
			source = b
		} else {
			target = b
		}
	}
	if err = f.repo.Invalidate(t.Context(), f.key, representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	responses := make(chan *http.Response, 1)
	failures := make(chan error, 1)
	go func() {
		resp, err := f.rt.RoundTrip(varyRequest(t, f.target, header))
		if err != nil {
			failures <- err
			return
		}
		responses <- resp
	}()
	select {
	case <-entered:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("validation did not start")
	}
	if err = f.repo.MigrateResource(t.Context(), f.key, target.ID()); err != nil {
		close(release)
		t.Fatal(err)
	}
	collect := func() {
		t.Helper()
		for range 8 {
			if _, err := manager.CollectUnreachable(t.Context(), 2); err != nil {
				t.Fatal(err)
			}
		}
	}
	collect()
	close(release)
	var resp *http.Response
	select {
	case resp = <-responses:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("304 response blocked")
	}
	collect()
	reader, _, err := source.ReadChunkFile(t.Context(), selected.Representation.ID, 0)
	if err != nil {
		t.Fatal("old body collected while HTTP reader active", err)
	}
	reader.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "original-image" || resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, string(body), err)
	}
	resp.Body.Close()
	collect()
	reader, _, err = source.ReadChunkFile(t.Context(), selected.Representation.ID, 0)
	if reader != nil {
		reader.Close()
	}
	if !os.IsNotExist(err) {
		t.Fatal("closed HTTP reader kept old files", err)
	}
	current, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || current.Fresh || current.Resource.Resource.ActiveLocation != target.ID() {
		t.Fatal("late 304 changed migrated root", fmt.Sprint(current), err)
	}
}

func TestRepresentationCancellationReleasesReadPinAndBackgroundOwnsDuplicate(t *testing.T) {
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) { originImage(w, "original-image", "Accept", `"v1"`) })
	_, _ = f.get(t, http.Header{"Accept": {"image/jpeg"}})
	pc := NewProcessorChain(NewStateProcessor(), NewVaryProcessor(), NewRevalidateProcessor())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := varyRequest(t, f.target, http.Header{"Accept": {"image/jpeg"}}).WithContext(ctx)
	c, err := pc.preCacheProcessor(nil, &representationTestStorage{bucket: f.bucket}, f.opt, req)
	if err != nil || !c.hit {
		t.Fatal(err)
	}
	background := c.cloneForBackground(req)
	if background.representationLease == nil || background.representationLease == c.representationLease {
		t.Fatal("background shares foreground pin")
	}
	resp, err := c.respondFromCache(req)
	if err != nil {
		t.Fatal(err)
	}
	c.finishRepresentationLease(resp)
	cancel()
	deadline := time.After(5 * time.Second)
	for {
		duplicate := c.representationLease.Duplicate()
		if duplicate == nil {
			break
		}
		duplicate.Close()
		select {
		case <-deadline:
			t.Fatal("cancel did not release read lease")
		case <-time.After(time.Millisecond):
		}
	}
	// The background clone was made before cancellation and owns a separate pin.
	duplicate := background.representationLease.Duplicate()
	if duplicate == nil {
		t.Fatal("foreground cancellation released background pin")
	}
	duplicate.Close()
	background.representationLease.Close()
	resp.Body.Close()
}
