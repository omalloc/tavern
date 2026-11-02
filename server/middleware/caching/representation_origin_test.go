package caching

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
	"github.com/omalloc/tavern/storage/representation"
)

type originFixture struct {
	rt     http.RoundTripper
	bucket storage.Bucket
	repo   *representation.IndexRepository
	key    object.ResourceKey
	target string
	clock  *atomic.Int64
	opt    *cachingOption
}

func newOriginFixture(t *testing.T, handler http.HandlerFunc) *originFixture {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(1800000000)
	_, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Unix(clock.Load(), 0).UTC().Format(http.TimeFormat))
		handler(w, r)
	})
	repo, err := representation.NewIndexRepository(bucket.(storage.ControlStore), bucket.ID(), func(ctx context.Context, location string, id *object.ID) (*object.Metadata, error) {
		if location != bucket.ID() {
			return nil, storage.ErrKeyNotFound
		}
		return bucket.Lookup(ctx, id)
	}, representation.WithClock(func() time.Time { return time.Unix(clock.Load(), 0) }))
	if err != nil {
		t.Fatal(err)
	}
	key, err := object.NewResourceKey(target, "http-v2", "origin")
	if err != nil {
		t.Fatal(err)
	}
	opt := &cachingOption{SliceSize: 64, IncludeQueryInCacheKey: true, VaryLimit: 16, Hostname: "test",
		representation: &representationLookup{repository: repo, namespace: "http-v2", isolationScope: "origin", writes: true}}
	rt := representationTestTransport(&representationTestStorage{bucket: bucket}, opt)
	return &originFixture{rt, bucket, repo, key, target, clock, opt}
}

func originImage(w http.ResponseWriter, body, vary, etag string) {
	w.Header().Set("Cache-Control", "max-age=600")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if vary != "" {
		w.Header().Set("Vary", vary)
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	fmt.Fprint(w, body)
}

func (f *originFixture) get(t *testing.T, header http.Header) (*http.Response, string) {
	t.Helper()
	resp, body, err := readVaryResponse(f.rt, varyRequest(t, f.target, header))
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func (f *originFixture) root(t *testing.T) *object.Metadata {
	t.Helper()
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRepresentationOriginCartesianAndVaryEvolution(t *testing.T) {
	t.Run("cartesian", func(t *testing.T) {
		var calls atomic.Int32
		f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			originImage(w, r.Header.Get("Accept")+":"+r.Header.Get("X-Theme"), "Accept, X-Theme", "")
		})
		for range 2 {
			for _, mime := range []string{"image/jpeg", "image/webp,image/jpeg;q=0.8"} {
				for _, theme := range []string{"light", "dark"} {
					_, body := f.get(t, http.Header{"Accept": {mime}, "X-Theme": {theme}})
					if body != mime+":"+theme {
						t.Fatal("crossed selection:", body)
					}
				}
			}
		}
		if calls.Load() != 4 || len(f.root(t).Resource.Variants) != 4 {
			t.Fatal("variants did not hit independently", calls.Load())
		}
	})
	t.Run("field changes", func(t *testing.T) {
		var phase atomic.Int32
		var calls atomic.Int32
		fields := []string{"", "Accept", "Accept, X-Theme", ""}
		f := newOriginFixture(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			n := phase.Load()
			originImage(w, fmt.Sprint("body-", n), fields[n], fmt.Sprintf(`"etag-%d"`, n))
		})
		for i, vary := range fields {
			phase.Store(int32(i))
			if i > 0 {
				f.clock.Add(601)
			}
			for range 2 {
				resp, body := f.get(t, http.Header{"Accept": {"image/jpeg"}, "X-Theme": {"dark"}})
				if body != fmt.Sprint("body-", i) || resp.Header.Get("Vary") != vary {
					t.Fatalf("phase %d: %q, %v", i, body, resp.Header)
				}
			}
			if len(f.root(t).Resource.Variants) != 1 {
				t.Fatal("field evolution retained selected obsolete reference")
			}
		}
		if calls.Load() != 4 {
			t.Fatal("field evolution skipped publication or inherited old Vary", calls.Load())
		}
	})
}

func TestRepresentationOriginBudgetBypassesOnlyNewSelector(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		originImage(w, r.Header.Get("Accept"), "Accept", "")
	})
	f.opt.VaryLimit = 2
	for _, accept := range []string{"a", "b"} {
		f.get(t, http.Header{"Accept": {accept}})
	}
	for range 2 {
		resp, body := f.get(t, http.Header{"Accept": {"c"}})
		if body != "c" || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "BYPASS") {
			t.Fatal(resp.Header, body)
		}
	}
	for _, accept := range []string{"a", "b"} {
		if _, body := f.get(t, http.Header{"Accept": {accept}}); body != accept {
			t.Fatal(body)
		}
	}
	if calls.Load() != 4 || len(f.root(t).Resource.Variants) != 2 {
		t.Fatal("limit deleted existing variants")
	}
	f.clock.Add(601)
	f.get(t, http.Header{"Accept": {"a"}})
	f.get(t, http.Header{"Accept": {"a"}})
	if calls.Load() != 5 || len(f.root(t).Resource.Variants) != 2 {
		t.Fatal("updating an existing selector consumed budget", calls.Load())
	}
}

func TestRepresentationOriginAdmissionBypass(t *testing.T) {
	for _, kind := range []string{"star", "invalid", "fields", "snapshot", "no-store", "private", "partial"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			f := newOriginFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Cache-Control", "max-age=600")
				w.Header().Set("Content-Length", "5")
				switch kind {
				case "star":
					w.Header().Set("Vary", "Accept, *")
				case "invalid":
					w.Header().Set("Vary", "bad field")
				case "fields":
					var fields []string
					for i := range 33 {
						fields = append(fields, fmt.Sprint("X-", i))
					}
					w.Header().Set("Vary", strings.Join(fields, ","))
				case "snapshot":
					w.Header().Set("Vary", "Accept")
				case "no-store", "private":
					w.Header().Set("Cache-Control", "max-age=600,"+kind)
					w.Header().Set(protocol.ProtocolCacheTime, "600")
				case "partial":
					w.Header().Set("Content-Range", "bytes 0-4/10")
					w.WriteHeader(206)
				}
				fmt.Fprint(w, "image")
			})
			header := http.Header{"Accept": {"image/jpeg"}}
			if kind == "snapshot" {
				header.Set("Accept", strings.Repeat("a", varycontrol.MaxSnapshotBytes))
			}
			for range 2 {
				if _, body := f.get(t, header); body != "image" {
					t.Fatal(body)
				}
			}
			if calls.Load() != 2 || len(f.root(t).Resource.Variants) != 0 {
				t.Fatal("bypass published a reference")
			}
			requireEmptyVaryCache(t, f.bucket)
		})
	}
}

func TestRepresentationValidationRetainsHeadersAgeAndBody(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Age", "120")
		if n == 1 {
			originImage(w, "jpeg bytes", "Accept", `"ABC"`)
			return
		}
		if r.Header.Get("If-None-Match") != `"ABC"` {
			t.Error("lost case-sensitive validator", r.Header)
		}
		// Cache-Control, Vary, Content-Type and ETag deliberately absent.
		w.WriteHeader(http.StatusNotModified)
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	f.get(t, header)
	first, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Add(481)
	resp, body := f.get(t, header)
	if body != "jpeg bytes" || resp.StatusCode != 200 || resp.Header.Get("Vary") != "Accept" || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatal(resp, body)
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Representation.ID.Hash() != first.Representation.ID.Hash() || selected.Representation.Representation.BodyGeneration != first.Representation.Representation.BodyGeneration {
		t.Fatal("304 rewrote body identity")
	}
	if selected.Representation.ExpiresAt != f.clock.Load()+480 || selected.Representation.Headers.Get("Age") != "120" {
		t.Fatal("304 reset origin age or TTL", selected.Representation)
	}
	f.get(t, header)
	if calls.Load() != 2 {
		t.Fatal("304 did not renew the selected reference", calls.Load())
	}
}

func TestRepresentationValidationChangesVary(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			originImage(w, "jpeg", "Accept", `"v1"`)
			return
		}
		if r.Header.Get("If-None-Match") != `"v1"` {
			t.Error("missing validator")
		}
		w.Header().Set("Age", "0")
		w.Header().Set("Vary", "Accept, X-Theme")
		w.WriteHeader(304)
	})
	header := http.Header{"Accept": {"image/jpeg"}, "X-Theme": {"dark"}}
	f.get(t, header)
	id := f.root(t).Resource.Variants[0].RepresentationID.Hash()
	f.clock.Add(601)
	resp, body := f.get(t, header)
	if body != "jpeg" || resp.Header.Get("Vary") != "Accept, X-Theme" {
		t.Fatal(resp.Header, body)
	}
	f.get(t, header)
	if calls.Load() != 2 {
		t.Fatal("changed Vary snapshot was not published")
	}
	root := f.root(t)
	if len(root.Resource.Variants) != 1 || root.Resource.Variants[0].RepresentationID.Hash() != id || len(root.Resource.Variants[0].VaryFields) != 2 {
		t.Fatal("304 lost body or index identity")
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"image/jpeg"}, "X-Theme": {"light"}}); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("304 reused a different theme", err)
	}
}

func TestRepresentationValidationCapturesActualUpstreamRequest(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			originImage(w, "old bytes", "Accept", `"v1"`)
		case 2:
			if r.Header.Get("If-None-Match") != `"v1"` {
				t.Error("missing validation field")
			}
			w.Header().Set("Age", "0")
			w.Header().Set("Vary", "If-None-Match")
			w.WriteHeader(304)
		default:
			originImage(w, "unconditional bytes", "If-None-Match", `"v1"`)
		}
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	f.get(t, header)
	f.clock.Add(601)
	if _, body := f.get(t, header); body != "old bytes" {
		t.Fatal(body)
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, header); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("snapshot used client headers instead of validation headers", err)
	}
	if _, body := f.get(t, header); body != "unconditional bytes" {
		t.Fatal(body)
	}
}

func TestRepresentationValidationRetriesInapplicableETag(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			originImage(w, "old", "Accept", `"ABC"`)
		case 2:
			w.Header().Set("ETag", `"abc"`)
			w.WriteHeader(304)
		default:
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
				t.Error("retry was conditional")
			}
			originImage(w, "new", "Accept", `"abc"`)
		}
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	f.get(t, header)
	f.clock.Add(601)
	if _, body := f.get(t, header); body != "new" {
		t.Fatal("case-folded ETag reused stale bytes", body)
	}
	if calls.Load() != 3 {
		t.Fatal("unconditional retry count", calls.Load())
	}
}

func TestRepresentationUnsolicited304IsBoundedAndClient304IsPreserved(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(304) })
	_, _, err := readVaryResponse(f.rt, varyRequest(t, f.target, nil))
	if err == nil || calls.Load() != 2 {
		t.Fatal("unsolicited 304 was accepted or retried indefinitely", calls.Load(), err)
	}
	resp, body := f.get(t, http.Header{"If-None-Match": {`"client"`}})
	if resp.StatusCode != 304 || body != "" || calls.Load() != 3 {
		t.Fatal("client 304 was converted to 200", resp, body)
	}
}

func waitOriginHeaders(t *testing.T, responses <-chan *http.Response, failures <-chan error) *http.Response {
	t.Helper()
	select {
	case resp := <-responses:
		return resp
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("origin headers did not arrive")
	}
	return nil
}

func TestRepresentationPendingBudgetReleasedByClose(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Accept") == "a" {
			w.Header().Set("Vary", "Accept")
			w.Header().Set("Cache-Control", "max-age=600")
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, "a")
			return
		}
		originImage(w, "b", "Accept", "")
	})
	f.opt.VaryLimit = 1
	responses, failures := make(chan *http.Response, 1), make(chan error, 1)
	req := varyRequest(t, f.target, http.Header{"Accept": {"a"}})
	go func() {
		resp, err := f.rt.RoundTrip(req)
		if err != nil {
			failures <- err
		} else {
			responses <- resp
		}
	}()
	pending := waitOriginHeaders(t, responses, failures)
	resp, _ := f.get(t, http.Header{"Accept": {"b"}})
	if !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "BYPASS") || len(f.root(t).Resource.Variants) != 0 {
		t.Fatal("pending selector was not counted")
	}
	unblock()
	_ = pending.Body.Close()
	f.get(t, http.Header{"Accept": {"b"}})
	f.get(t, http.Header{"Accept": {"b"}})
	if calls.Load() != 3 || len(f.root(t).Resource.Variants) != 1 {
		t.Fatal("closed fill leaked its reservation", calls.Load())
	}
}

func TestRepresentationLateOlderResponseCannotReplaceNewer(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Vary", "Accept")
			w.Header().Set("Cache-Control", "max-age=600")
			w.Header().Set("Content-Length", "3")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, "old")
			return
		}
		w.Header().Set("Date", time.Unix(1800000001, 0).UTC().Format(http.TimeFormat))
		originImage(w, "new", "Accept", "")
	})
	responses, failures := make(chan *http.Response, 1), make(chan error, 1)
	header := http.Header{"Accept": {"image/jpeg"}}
	req := varyRequest(t, f.target, header)
	go func() {
		resp, err := f.rt.RoundTrip(req)
		if err != nil {
			failures <- err
		} else {
			responses <- resp
		}
	}()
	old := waitOriginHeaders(t, responses, failures)
	if _, body := f.get(t, header); body != "new" {
		t.Fatal(body)
	}
	unblock()
	body, err := io.ReadAll(old.Body)
	_ = old.Body.Close()
	if err != nil || string(body) != "old" {
		t.Fatal(string(body), err)
	}
	if _, body := f.get(t, header); body != "new" {
		t.Fatal("late response replaced new candidate", body)
	}
	if calls.Load() != 2 || len(f.root(t).Resource.Variants) != 1 {
		t.Fatal(calls.Load())
	}
}

func TestRepresentationConcurrentOriginVariantsMerge(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		mime := r.Header.Get("Accept")
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(mime)))
		w.Header().Set("Vary", "Accept")
		w.Header().Set("Cache-Control", "s-maxage=600")
		fmt.Fprint(w, mime)
	})
	type result struct {
		resp *http.Response
		body string
		err  error
		mime string
	}
	results := make(chan result, 2)
	for _, mime := range []string{"image/jpeg", "image/webp"} {
		req := varyRequest(t, f.target, http.Header{"Accept": {mime}})
		go func() {
			resp, body, err := readVaryResponse(f.rt, req)
			results <- result{resp, body, err, req.Header.Get("Accept")}
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("requests did not overlap at origin")
		}
	}
	unblock()
	for range 2 {
		r := <-results
		if r.err != nil || r.body != r.mime || r.resp.Header.Get("Content-Type") != r.mime {
			t.Fatal(r)
		}
	}
	for _, mime := range []string{"image/jpeg", "image/webp"} {
		if _, body := f.get(t, http.Header{"Accept": {mime}}); body != mime {
			t.Fatal(body)
		}
	}
	if calls.Load() != 2 || len(f.root(t).Resource.Variants) != 2 {
		t.Fatal("concurrent publication lost a reference", calls.Load())
	}
}

func TestRepresentationValidationMissingBodyRetries(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			originImage(w, "old bytes", "Accept", `"old"`)
		case 2:
			w.Header().Set("ETag", `"old"`)
			w.WriteHeader(304)
		default:
			if r.Header.Get("If-None-Match") != "" {
				t.Error("missing-body retry retained validator")
			}
			originImage(w, "new bytes", "Accept", `"new"`)
		}
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	f.get(t, header)
	f.clock.Add(601)
	broken := &missingRepresentationBucket{Bucket: f.bucket}
	f.rt = representationTestTransport(&representationTestStorage{bucket: broken}, f.opt)
	resp, body := f.get(t, header)
	if resp.StatusCode != 200 || body != "new bytes" || calls.Load() != 3 {
		t.Fatal(resp, body, calls.Load())
	}
}

func TestRepresentationValidationMissingAgeRenewsFreshness(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Age", "120")
			originImage(w, "image", "Accept", `"v1"`)
			return
		}
		w.WriteHeader(304)
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	f.get(t, header)
	f.clock.Add(481)
	resp, body := f.get(t, header)
	age, _ := strconv.ParseInt(resp.Header.Get("Age"), 10, 64)
	if body != "image" || age != 0 {
		t.Fatal("validation retained the previous response's age", body, resp.Header)
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || !selected.Fresh || selected.Representation.ExpiresAt != f.clock.Load()+600 {
		t.Fatal("successful validation did not renew TTL", err)
	}
	f.get(t, header)
	if calls.Load() != 2 {
		t.Fatal("validated response missed the cache", calls.Load())
	}
}

type failRepresentationClose struct {
	io.WriteCloser
	fail bool
}

func (w *failRepresentationClose) Close() error {
	err := w.WriteCloser.Close()
	if err == nil && w.fail {
		return errors.New("injected chunk close failure")
	}
	return err
}

type failingRepresentationBucket struct {
	storage.Bucket
	fail   atomic.Bool
	lastID atomic.Pointer[object.ID]
}

func (b *failingRepresentationBucket) WriteChunkFile(ctx context.Context, id *object.ID, index uint32) (io.WriteCloser, string, error) {
	writer, path, err := b.Bucket.WriteChunkFile(ctx, id, index)
	if err != nil {
		return nil, path, err
	}
	b.lastID.Store(id.Clone())
	return &failRepresentationClose{writer, b.fail.Load()}, path, nil
}

func TestRepresentationWriteFailurePreservesResponseAndReleasesBudget(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		originImage(w, strings.Repeat(r.Header.Get("Accept"), 64), "Accept", "")
	})
	broken := &failingRepresentationBucket{Bucket: f.bucket}
	broken.fail.Store(true)
	f.opt.VaryLimit = 1
	f.rt = representationTestTransport(&representationTestStorage{bucket: broken}, f.opt)
	if _, body := f.get(t, http.Header{"Accept": {"a"}}); body != strings.Repeat("a", 64) {
		t.Fatal("cache failure damaged origin bytes", body)
	}
	if len(f.root(t).Resource.Variants) != 0 {
		t.Fatal("failed chunk became readable")
	}
	id := broken.lastID.Load()
	if id == nil {
		t.Fatal("failure did not exercise chunk ownership")
	}
	if file, _, err := f.bucket.ReadChunkFile(t.Context(), id, 0); !errors.Is(err, os.ErrNotExist) {
		if file != nil {
			file.Close()
		}
		t.Fatal("unconfirmed file leaked", err)
	}
	broken.fail.Store(false)
	f.get(t, http.Header{"Accept": {"b"}})
	f.get(t, http.Header{"Accept": {"b"}})
	if calls.Load() != 2 {
		t.Fatal("failed fill leaked its selector reservation", calls.Load())
	}
}

func TestRepresentationColdHeadAndUnalignedRangeDoNotPublishBodies(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Length", "2")
			w.Header().Set("Content-Range", "bytes 1-2/5")
			w.WriteHeader(206)
			fmt.Fprint(w, "ma")
			return
		}
		originImage(w, "image", "Accept", "")
	})
	req := varyRequest(t, f.target, http.Header{"Accept": {"image/jpeg"}})
	req.Method = http.MethodHead
	resp, body, err := readVaryResponse(f.rt, req)
	if err != nil || resp.StatusCode != 200 || body != "" {
		t.Fatal(resp, body, err)
	}
	resp, body = f.get(t, http.Header{"Range": {"bytes=1-2"}, "Accept": {"image/jpeg"}})
	if resp.StatusCode != 206 || body != "ma" {
		t.Fatal(resp, body)
	}
	if len(f.root(t).Resource.Variants) != 0 {
		t.Fatal("HEAD or an unaligned fragment published a body descriptor")
	}
	f.get(t, http.Header{"Accept": {"image/jpeg"}})
	req = varyRequest(t, f.target, http.Header{"Accept": {"image/jpeg"}})
	req.Method = http.MethodHead
	if _, _, err := readVaryResponse(f.rt, req); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal("HEAD did not reuse the complete GET body", calls.Load())
	}
}

func TestRepresentationValidationLastModified(t *testing.T) {
	var calls atomic.Int32
	modified := time.Unix(1700000000, 0).UTC().Format(http.TimeFormat)
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Last-Modified", modified)
			originImage(w, "image", "Accept", "")
			return
		}
		if r.Header.Get("If-Modified-Since") != modified || r.Header.Get("If-None-Match") != "" {
			t.Error("incorrect validation request", r.Header)
		}
		w.Header().Set("Age", "0")
		w.WriteHeader(304)
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	f.get(t, header)
	f.clock.Add(601)
	if _, body := f.get(t, header); body != "image" {
		t.Fatal(body)
	}
	f.get(t, header)
	if calls.Load() != 2 {
		t.Fatal("Last-Modified validation did not renew metadata", calls.Load())
	}
}

func TestRepresentationOriginVaryHostUsesRequestAuthority(t *testing.T) {
	for _, vary := range []string{"", "Host"} {
		t.Run("vary="+vary, func(t *testing.T) {
			var calls atomic.Int32
			f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); originImage(w, r.Host, vary, "") })
			for range 2 {
				for _, host := range []string{"tenant-a.example", "tenant-b.example"} {
					req := varyRequest(t, f.target, nil)
					req.Host = host
					_, body, err := readVaryResponse(f.rt, req)
					if err != nil || body != host {
						t.Fatal("crossed request authority", body, err)
					}
					if vary == "Host" {
						publicURL := *req.URL
						publicURL.Host = host
						key, _ := object.NewResourceKey(publicURL.String(), "http-v2", "origin")
						root, err := f.repo.Load(t.Context(), key)
						if err != nil {
							t.Fatal(err)
						}
						field := root.Resource.Variants[0].SelectorSnapshot.Fields[0]
						if !field.Present || len(field.Values) != 1 || field.Values[0] != host {
							t.Fatal("Host was absent from actual snapshot", field)
						}
					}
				}
			}
			if calls.Load() != 2 {
				t.Fatal("authority variants did not hit", calls.Load())
			}
		})
	}
}
