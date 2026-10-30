package caching

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"github.com/omalloc/tavern/pkg/lifecycle"
	"github.com/omalloc/tavern/server/middleware"
	"github.com/omalloc/tavern/storage/representation"
	planning "github.com/omalloc/tavern/transform"
)

type testImageInspector struct{ calls atomic.Int32 }

func (i *testImageInspector) Version() string { return "test-jpeg-png-v1" }
func (i *testImageInspector) Inspect(ctx context.Context, path string, size, maxPixels int64) (transformv1.SourceInfo, error) {
	i.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return transformv1.SourceInfo{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return transformv1.SourceInfo{}, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return transformv1.SourceInfo{}, err
	}
	if config.Height <= 0 || int64(config.Width) > maxPixels/int64(config.Height) {
		return transformv1.SourceInfo{}, planning.ErrBudget
	}
	decoded, actual, err := image.Decode(bytes.NewReader(data))
	if err != nil || actual != format {
		return transformv1.SourceInfo{}, planning.ErrUnsupportedSource
	}
	opaque := true
	if image, ok := decoded.(interface{ Opaque() bool }); ok {
		opaque = image.Opaque()
	}
	return transformv1.SourceInfo{MIME: "image/" + format, Size: size, Width: config.Width, Height: config.Height, HasAlpha: !opaque}, nil
}

type testImageBackend struct {
	inspector        *testImageInspector
	dir              string
	calls            atomic.Int32
	entered, release chan struct{}
	once             sync.Once
	failure          atomic.Bool
}

func (b *testImageBackend) Capabilities() transformv1.BackendCapabilities {
	return transformv1.BackendCapabilities{SchemaVersion: 1, Inputs: []string{"image/jpeg", "image/png"}, Outputs: []transformv1.Codec{{MIME: "image/jpeg", QualityMin: 1, QualityMax: 100}, {MIME: "image/png", Lossless: true, CompressionMax: 9}}, Conversions: []transformv1.Conversion{{InputMIME: "image/jpeg", OutputMIME: "image/png"}, {InputMIME: "image/jpeg", OutputMIME: "image/jpeg"}, {InputMIME: "image/png", OutputMIME: "image/jpeg"}, {InputMIME: "image/png", OutputMIME: "image/png"}}, PolicyVersion: "test-p1", EncoderVersion: "test-e1"}
}
func (b *testImageBackend) Transform(ctx context.Context, source transformv1.CompleteSource, spec transformv1.TransformSpec) (transformv1.CompleteOutput, error) {
	b.calls.Add(1)
	if b.entered != nil {
		b.once.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if b.failure.Load() {
		return nil, errors.New("injected encoder failure")
	}
	reader, err := source.Open(ctx)
	if err != nil {
		return nil, err
	}
	decoded, _, err := image.Decode(reader)
	reader.Close()
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	switch spec.OutputMIME {
	case "image/png":
		err = png.Encode(&output, decoded)
	case "image/jpeg":
		err = jpeg.Encode(&output, decoded, &jpeg.Options{Quality: *spec.Quality})
	default:
		err = planning.ErrUnsupportedSource
	}
	if err != nil {
		return nil, err
	}
	result, err := planning.SpoolDecoded(ctx, &output, "identity", b.dir, 1<<20, 1000, b.inspector)
	if err != nil {
		return nil, err
	}
	result.SetEncoderVersion("test-e1")
	return result, nil
}

type managedTestStore struct {
	*representationTestStorage
	manager *representation.Manager
	work    lifecycle.Group
	purgers map[string]storage.RepresentationPurger
	purgeMu sync.Mutex
}

func (s *managedTestStore) RepresentationManager() *representation.Manager { return s.manager }
func (s *managedTestStore) BeginCacheTask(ctx context.Context, timeout time.Duration) (context.Context, func(), error) {
	return s.work.Begin(ctx, timeout)
}
func (s *managedTestStore) Drain(ctx context.Context) error { return s.work.Drain(ctx) }
func (s *managedTestStore) RegisterRepresentationPurger(name string, purger storage.RepresentationPurger) error {
	s.purgeMu.Lock()
	defer s.purgeMu.Unlock()
	if s.purgers == nil {
		s.purgers = make(map[string]storage.RepresentationPurger)
	}
	if s.purgers[name] != nil {
		return errors.New("duplicate purger")
	}
	s.purgers[name] = purger
	return nil
}
func (s *managedTestStore) PURGE(raw string, control storage.PurgeControl) error {
	s.purgeMu.Lock()
	defer s.purgeMu.Unlock()
	failures := []error{s.manager.Purge(context.Background(), raw, control)}
	for _, purger := range s.purgers {
		failures = append(failures, purger.Purge(context.Background(), raw, control))
	}
	return errors.Join(failures...)
}

type managedFixture struct {
	provider                     *managedProvider
	store                        *managedTestStore
	router                       *planning.Router
	backend                      *testImageBackend
	inspector                    *testImageInspector
	directory                    string
	origins                      atomic.Int32
	sourceReads                  atomic.Int32
	clock                        atomic.Int64
	raw                          []byte
	mu                           sync.Mutex
	encoding, cacheControl, etag string
	status304                    bool
}

type sourceReadBucket struct {
	storage.Bucket
	reads     *atomic.Int32
	sourceIDs sync.Map
}

func (b *sourceReadBucket) ReadChunkFile(ctx context.Context, id *object.ID, index uint32) (storage.File, string, error) {
	if _, ok := b.sourceIDs.Load(id.Hash()); ok {
		b.reads.Add(1)
	}
	return b.Bucket.ReadChunkFile(ctx, id, index)
}
func (b *sourceReadBucket) Store(ctx context.Context, md *object.Metadata) error {
	if md.Representation != nil && md.Representation.Kind == object.RepresentationOrigin {
		b.sourceIDs.Store(md.ID.Hash(), true)
	}
	return b.Bucket.Store(ctx, md)
}
func (b *sourceReadBucket) LoadControl(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	return b.Bucket.(storage.ControlStore).LoadControl(ctx, id)
}
func (b *sourceReadBucket) StoreControl(ctx context.Context, md *object.Metadata) error {
	return b.Bucket.(storage.ControlStore).StoreControl(ctx, md)
}
func (b *sourceReadBucket) ScanMetadata(ctx context.Context, cursor []byte, limit int) ([]*object.Metadata, []byte, error) {
	return b.Bucket.(storage.MetadataScanner).ScanMetadata(ctx, cursor, limit)
}
func (b *sourceReadBucket) SetRepresentationEvictor(fn func(context.Context, *object.Metadata) error) {
	b.Bucket.(storage.RepresentationEviction).SetRepresentationEvictor(fn)
}

func newManagedFixture(t *testing.T) *managedFixture {
	t.Helper()
	_, bucket, _ := newVaryTestCache(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected global origin") })
	f := &managedFixture{directory: t.TempDir(), cacheControl: "max-age=600", etag: `"source-v1"`}
	f.clock.Store(time.Now().Unix())
	canvas := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			canvas.Set(x, y, color.RGBA{R: 80, G: 130, B: 190, A: 255})
		}
	}
	var data bytes.Buffer
	jpeg.Encode(&data, canvas, &jpeg.Options{Quality: 90})
	f.raw = data.Bytes()
	wrapped := &sourceReadBucket{Bucket: bucket, reads: &f.sourceReads}
	manager, err := representation.NewManager(t.Context(), []storage.Bucket{wrapped}, representation.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	f.store = &managedTestStore{representationTestStorage: &representationTestStorage{bucket: wrapped}, manager: manager}
	f.inspector = &testImageInspector{}
	f.backend = &testImageBackend{inspector: f.inspector, dir: f.directory}
	origin := middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		f.origins.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		if req.Header.Get("Accept") != "image/jpeg, image/png" || req.Header.Get("Accept-Encoding") != "identity" || req.Header.Get("Range") != "" || strings.Contains(req.URL.RawQuery, "format=") || strings.Contains(req.URL.RawQuery, "quality=") {
			t.Errorf("source request used target selection: %s %v", req.URL, req.Header)
		}
		if req.Header.Get("X-Tavern-Transform-Spec") != "" {
			t.Error("client pseudo-internal header forwarded")
		}
		status := 200
		body := append([]byte(nil), f.raw...)
		headers := http.Header{"Cache-Control": {f.cacheControl}, "Content-Type": {"image/jpeg"}, "Vary": {"Accept, X-Tenant"}, "Date": {time.Unix(f.clock.Load(), 0).UTC().Format(http.TimeFormat)}, "Etag": {f.etag}}
		if f.status304 && req.Header.Get("If-None-Match") == f.etag {
			status = 304
			body = nil
			headers.Del("Content-Type")
		} else if f.encoding != "" {
			var encoded bytes.Buffer
			switch f.encoding {
			case "gzip":
				writer := gzip.NewWriter(&encoded)
				writer.Write(body)
				writer.Close()
			case "br":
				writer := brotli.NewWriter(&encoded)
				writer.Write(body)
				writer.Close()
			}
			body = encoded.Bytes()
			headers.Set("Content-Encoding", f.encoding)
		}
		if status != 304 {
			headers.Set("Content-Length", strconv.Itoa(len(body)))
		}
		return &http.Response{StatusCode: status, Header: headers, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})
	opt := cachingOption{SliceSize: 1024, VaryLimit: 20, representation: &representationLookup{namespace: "http-v2", isolationScope: "origin"}}
	f.provider, err = newManagedProvider(f.store, origin, opt, f.backend, f.inspector, managedLimits{InputBytes: 1 << 20, OutputBytes: 1 << 20, Pixels: 1000, Timeout: 5 * time.Second, Directory: f.directory})
	if err != nil {
		t.Fatal(err)
	}
	f.router, err = planning.NewRouter([]planning.Route{{PathPrefix: "/images/", Policy: transformv1.Policy{Formats: []string{"image/png", "image/jpeg"}, PolicyVersion: "route-v1"}}}, f.backend.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.provider.bindRoutes(f.router); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.store.Drain(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}
func (f *managedFixture) request(t *testing.T, query, accept string) (*http.Request, planning.Plan) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images/a.jpg?"+query, nil)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-Tenant", "tenant-a")
	plan, ok, err := f.router.Resolve(req)
	if err != nil || !ok {
		t.Fatal(plan, ok, err)
	}
	return req, plan
}
func (f *managedFixture) get(t *testing.T, query, accept string) (*managedResult, []byte) {
	t.Helper()
	req, plan := f.request(t, query, accept)
	result, err := f.provider.resolve(req, plan)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := result.Open(t.Context())
	if err != nil {
		result.Close()
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	result.Close()
	if err != nil {
		t.Fatal(err)
	}
	return result, body
}
func assertNoImageTemporaryFiles(t *testing.T, f *managedFixture) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(f.directory, "tavern-image-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("image temporary files leaked", files, err)
	}
}

func TestManagedProviderCoalescesSourcesAndProducts(t *testing.T) {
	f := newManagedFixture(t)
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	bodies := make(chan []byte, 20)
	for i := range 20 {
		wg.Go(func() {
			query, accept := "format=png", "image/png"
			if i%2 == 1 {
				accept = "image/jpeg;q=0.4, image/png"
			}
			req, plan := f.request(t, query, accept)
			result, err := f.provider.resolve(req, plan)
			if err != nil {
				errors <- err
				return
			}
			defer result.Close()
			reader, err := result.Open(t.Context())
			if err != nil {
				errors <- err
				return
			}
			body, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				errors <- err
				return
			}
			bodies <- body
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	close(bodies)
	var first []byte
	for body := range bodies {
		if first == nil {
			first = body
		} else if !bytes.Equal(first, body) {
			t.Fatal("coalesced output bytes differed")
		}
	}
	if f.origins.Load() != 1 || f.backend.calls.Load() != 1 {
		t.Fatal("unexpected call counts", f.origins.Load(), f.backend.calls.Load())
	}
	beforeReads, beforeInspect := f.sourceReads.Load(), f.inspector.calls.Load()
	hit, body := f.get(t, "format=png", "image/png")
	if !hit.Cached || !bytes.Equal(first, body) || f.sourceReads.Load() != beforeReads || f.inspector.calls.Load() != beforeInspect || f.backend.calls.Load() != 1 {
		t.Fatal("derived hit opened/decoded source or transformed")
	}
	f.get(t, "format=jpeg&quality=70", "image/jpeg")
	if f.origins.Load() != 1 || f.backend.calls.Load() != 2 {
		t.Fatal("another target failed to reuse source", f.origins.Load(), f.backend.calls.Load())
	}
	assertNoImageTemporaryFiles(t, f)
}
func TestManagedProviderSource304KeepsProductsAnd200ReplacesThem(t *testing.T) {
	f := newManagedFixture(t)
	first, _ := f.get(t, "format=png", "image/png")
	req, plan := f.request(t, "format=png", "image/png")
	sourceReq, _ := f.provider.sourceRequest(req, plan)
	c := &Caching{opt: &f.provider.opt}
	key, _ := c.representationKey(sourceReq)
	repo, _ := f.store.manager.Repository(key)
	if err := repo.Invalidate(t.Context(), key, representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.status304 = true
	f.clock.Add(2)
	f.mu.Unlock()
	second, _ := f.get(t, "format=png", "image/png")
	if !second.Cached || first.Metadata.ID.Hash() != second.Metadata.ID.Hash() || f.origins.Load() != 2 || f.backend.calls.Load() != 1 {
		t.Fatal("source 304 converted again", f.origins.Load(), f.backend.calls.Load())
	}
	if err := repo.Invalidate(t.Context(), key, representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.status304 = false
	f.etag = `"source-v2"`
	f.clock.Add(2)
	f.mu.Unlock()
	third, _ := f.get(t, "format=png", "image/png")
	if third.Metadata.ID.Hash() == first.Metadata.ID.Hash() || f.origins.Load() != 3 || f.backend.calls.Load() != 2 {
		t.Fatal("200 reused old content generation")
	}
	assertNoImageTemporaryFiles(t, f)
}
func TestManagedProviderCompressedSourceAndPrivateIsolation(t *testing.T) {
	for _, encoding := range []string{"gzip", "br"} {
		t.Run(encoding, func(t *testing.T) {
			f := newManagedFixture(t)
			f.encoding = encoding
			result, data := f.get(t, "format=png", "image/png")
			if result.Metadata.Headers.Get("Content-Encoding") != "" || !bytes.HasPrefix(data, []byte("\x89PNG")) {
				t.Fatal("encoded origin bytes used as pixels")
			}
			f.get(t, "format=png", "image/png")
			if f.origins.Load() != 1 || f.backend.calls.Load() != 1 {
				t.Fatal("compressed source not reused")
			}
			assertNoImageTemporaryFiles(t, f)
		})
	}
	f := newManagedFixture(t)
	f.cacheControl = "private, max-age=600"
	for range 2 {
		result, _ := f.get(t, "format=png", "image/png")
		if result.Cached || result.Metadata.Headers.Get("Cache-Control") != "no-store" || result.Metadata.ID != nil {
			t.Fatal("private source became public output")
		}
	}
	if f.origins.Load() != 2 || f.backend.calls.Load() != 2 {
		t.Fatal("private source/output shared across requests")
	}
	assertNoImageTemporaryFiles(t, f)
}
func TestManagedProviderPurgeRejectsLateTransform(t *testing.T) {
	f := newManagedFixture(t)
	f.backend.entered, f.backend.release = make(chan struct{}), make(chan struct{})
	req, plan := f.request(t, "format=png", "image/png")
	done := make(chan error, 1)
	go func() {
		result, err := f.provider.resolve(req, plan)
		if result != nil {
			result.Close()
		}
		done <- err
	}()
	select {
	case <-f.backend.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("transform did not start")
	}
	if err := f.store.manager.Purge(t.Context(), "https://example.com/images/", storage.PurgeControl{Dir: true, Hard: true}); err != nil {
		t.Fatal(err)
	}
	close(f.backend.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.origins.Load() != 2 || f.backend.calls.Load() != 2 {
		t.Fatal("old task survived purge as a fresh hit", f.origins.Load(), f.backend.calls.Load())
	}
	f.get(t, "format=png", "image/png")
	if f.backend.calls.Load() != 2 {
		t.Fatal("replacement did not publish")
	}
	assertNoImageTemporaryFiles(t, f)
}
func TestManagedProviderFailureReleasesBudgetAndTemporaryFiles(t *testing.T) {
	f := newManagedFixture(t)
	f.backend.failure.Store(true)
	f.provider.opt.VaryLimit = 1
	req, plan := f.request(t, "format=png", "image/png")
	if result, err := f.provider.resolve(req, plan); err == nil {
		result.Close()
		t.Fatal("encoder failure ignored")
	}
	assertNoImageTemporaryFiles(t, f)
	f.backend.failure.Store(false)
	f.get(t, "format=jpeg&quality=70", "image/jpeg")
	assertNoImageTemporaryFiles(t, f)
}

func TestManagedProviderDifferentTargetsConcurrentlyShareSource(t *testing.T) {
	f := newManagedFixture(t)
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			query, accept := "format=png", "image/png"
			if i%2 == 1 {
				query, accept = "format=jpeg&quality=70", "image/jpeg"
			}
			req, plan := f.request(t, query, accept)
			result, err := f.provider.resolve(req, plan)
			if err != nil {
				failures <- err
				return
			}
			reader, err := result.Open(t.Context())
			if err == nil {
				_, err = io.Copy(io.Discard, reader)
				reader.Close()
			}
			result.Close()
			if err != nil {
				failures <- err
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if f.origins.Load() != 1 || f.backend.calls.Load() != 2 {
		t.Fatal("did not layer source and transform work", f.origins.Load(), f.backend.calls.Load())
	}
	assertNoImageTemporaryFiles(t, f)
}
func TestManagedProviderFilePurgeUsesSameSourceMapping(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(strconv.FormatBool(hard), func(t *testing.T) {
			f := newManagedFixture(t)
			first, _ := f.get(t, "format=png", "image/png")
			f.get(t, "format=jpeg&quality=70", "image/jpeg")
			f.mu.Lock()
			f.status304 = !hard
			f.mu.Unlock()
			if err := f.store.PURGE("https://example.com/images/a.jpg?format=png", storage.PurgeControl{Hard: hard}); err != nil {
				t.Fatal(err)
			}
			result, _ := f.get(t, "format=png", "image/png")
			if f.origins.Load() != 2 {
				t.Fatal("mapped file purge missed source namespace", f.origins.Load())
			}
			if !hard && (result.Metadata.ID.Hash() != first.Metadata.ID.Hash() || f.backend.calls.Load() != 2) {
				t.Fatal("soft validation did not retain source version")
			}
			if hard && (result.Metadata.ID.Hash() == first.Metadata.ID.Hash() || f.backend.calls.Load() != 3) {
				t.Fatal("hard purge reused old product")
			}
			assertNoImageTemporaryFiles(t, f)
		})
	}
}
func TestManagedProviderCancellationDoesNotCancelAnotherWaiter(t *testing.T) {
	f := newManagedFixture(t)
	f.backend.entered, f.backend.release = make(chan struct{}), make(chan struct{})
	req, plan := f.request(t, "format=png", "image/png")
	ctx, cancel := context.WithCancel(req.Context())
	first := make(chan error, 1)
	go func() {
		result, err := f.provider.resolve(req.WithContext(ctx), plan)
		if result != nil {
			result.Close()
		}
		first <- err
	}()
	select {
	case <-f.backend.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("encoder did not start")
	}
	second := make(chan error, 1)
	go func() {
		result, err := f.provider.resolve(req, plan)
		if result != nil {
			result.Close()
		}
		second <- err
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(f.backend.release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if f.origins.Load() != 1 || f.backend.calls.Load() != 1 {
		t.Fatal("one reader cancelled shared accepted work", f.origins.Load(), f.backend.calls.Load())
	}
	drainCtx, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	if err := f.store.Drain(drainCtx); err != nil {
		t.Fatal(err)
	}
	assertNoImageTemporaryFiles(t, f)
	f.provider.sources.mu.Lock()
	sources := len(f.provider.sources.entries)
	f.provider.sources.mu.Unlock()
	f.provider.products.mu.Lock()
	products := len(f.provider.products.entries)
	f.provider.products.mu.Unlock()
	if sources != 0 || products != 0 {
		t.Fatal("work gates leaked", sources, products)
	}
}
func TestManagedProviderWorkTimeoutReleasesReservation(t *testing.T) {
	f := newManagedFixture(t)
	f.provider.opt.VaryLimit = 1
	f.provider.limits.Timeout = 25 * time.Millisecond
	f.backend.entered, f.backend.release = make(chan struct{}), make(chan struct{})
	req, plan := f.request(t, "format=png", "image/png")
	if result, err := f.provider.resolve(req, plan); !errors.Is(err, context.DeadlineExceeded) {
		if result != nil {
			result.Close()
		}
		t.Fatal("work timeout not enforced", err)
	}
	f.provider.limits.Timeout = 5 * time.Second
	close(f.backend.release)
	f.get(t, "format=jpeg&quality=70", "image/jpeg")
	assertNoImageTemporaryFiles(t, f)
}

func TestCachingReadsTrustedManagedPlanBeforeOrdinaryLookup(t *testing.T) {
	f := newManagedFixture(t)
	opt := &cachingOption{SliceSize: 1024, VaryLimit: 20, Hostname: "test", managed: f.provider}
	rt := bindCaching(f.provider.origin, f.store, opt, NewProcessorChain(NewStateProcessor()))
	for range 2 {
		req, plan := f.request(t, "format=png", "image/png")
		req = req.WithContext(planning.WithPlan(req.Context(), plan))
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.Header.Get("Content-Type") != "image/png" || !bytes.HasPrefix(body, []byte("\x89PNG")) {
			t.Fatal("plan became transparent origin response", resp.Header, err)
		}
	}
	if f.origins.Load() != 1 || f.backend.calls.Load() != 1 {
		t.Fatal("pipeline did not use provider", f.origins.Load(), f.backend.calls.Load())
	}
	opt.managed = nil
	req, plan := f.request(t, "format=png", "image/png")
	req = req.WithContext(planning.WithPlan(req.Context(), plan))
	if resp, err := rt.RoundTrip(req); err == nil {
		closeBody(resp)
		t.Fatal("unbound plan silently fell back to ordinary cache")
	}
	assertNoImageTemporaryFiles(t, f)
}

func TestManagedProviderPrivateOutputBoundsWholeTask(t *testing.T) {
	f := newManagedFixture(t)
	f.cacheControl = "private, max-age=600"
	f.provider.limits.Timeout = 100 * time.Millisecond
	var err error
	f.provider.budget, err = planning.NewWorkBudget(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	req, plan := f.request(t, "format=png", "image/png")
	first, err := f.provider.resolve(req, plan)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err = f.provider.resolve(req, plan); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("private output did not retain whole-task budget: %v", err)
	}
	if f.origins.Load() != 1 {
		t.Fatal("queued task fetched/decoded source outside its budget")
	}
	first.Close()
	next, err := f.provider.resolve(req, plan)
	if err != nil {
		t.Fatalf("completed output leaked permit: %v", err)
	}
	next.Close()
	if f.origins.Load() != 2 {
		t.Fatal("released task was not executed")
	}
	entries, err := os.ReadDir(f.directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private files leaked: %v %v", entries, err)
	}
}

func TestManagedProviderHTTPWorkerCompletePublication(t *testing.T) {
	f := newManagedFixture(t)
	var rpc atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpc.Add(1)
		encoded, err := base64.RawURLEncoding.DecodeString(r.Header.Get(planning.SpecHeader))
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid spec", 400)
			return
		}
		var spec transformv1.TransformSpec
		if err = json.Unmarshal(encoded, &spec); err != nil {
			t.Error(err)
			http.Error(w, "invalid spec", 400)
			return
		}
		decoded, _, err := image.Decode(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid source", 400)
			return
		}
		var output bytes.Buffer
		if spec.OutputMIME == "image/png" {
			err = png.Encode(&output, decoded)
		} else {
			err = jpeg.Encode(&output, decoded, &jpeg.Options{Quality: *spec.Quality})
		}
		if err != nil {
			t.Error(err)
			http.Error(w, "encoding failed", 500)
			return
		}
		w.Header().Set("Content-Type", spec.OutputMIME)
		w.Header().Set(planning.EncoderHeader, "test-e1")
		w.Write(output.Bytes())
	}))
	defer server.Close()
	worker, err := planning.NewHTTPWorker(planning.HTTPWorkerConfig{Endpoint: server.URL + "/v1/transform", Capabilities: f.backend.Capabilities(), Limits: planning.WorkerLimits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxPixels: 1000, Concurrency: 2, QueueSize: 32, Timeout: time.Second, TemporaryDirectory: f.directory}}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	probeCalls := rpc.Load()
	f.provider.backend = worker
	f.provider.caps = worker.Capabilities()
	f.provider.inspector = planning.ImageInspector{}
	_, first := f.get(t, "format=png", "image/png")
	result, second := f.get(t, "format=png", "image/png")
	if !bytes.Equal(first, second) || !result.Cached || f.origins.Load() != 1 || rpc.Load() != probeCalls+1 {
		t.Fatalf("HTTP conversion was not fully published/reused: origin=%d rpc=%d", f.origins.Load(), rpc.Load()-probeCalls)
	}
	entries, err := os.ReadDir(f.directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("worker/cache temp files leaked: %v %v", entries, err)
	}
}
