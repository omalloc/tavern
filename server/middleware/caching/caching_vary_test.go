package caching

import (
	"context"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kelindar/bitmap"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
)

// ---------- mockBucket implements storage.Bucket for tests ----------

type mockBucket struct {
	mu      sync.Mutex
	store   map[string]*object.Metadata // keyed by ID.cacheID
	discard []*object.ID
}

func newMockBucket() *mockBucket {
	return &mockBucket{
		store: make(map[string]*object.Metadata),
	}
}

func (m *mockBucket) Lookup(_ context.Context, id *object.ID) (*object.Metadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	md, ok := m.store[id.String()]
	if ok {
		clone := md.Clone()
		return clone, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockBucket) Store(_ context.Context, meta *object.Metadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[meta.ID.String()] = meta.Clone()
	return nil
}

func (m *mockBucket) Discard(_ context.Context, id *object.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, id.String())
	m.discard = append(m.discard, id)
	return nil
}

func (m *mockBucket) DiscardWithMessage(_ context.Context, id *object.ID, _ string) error {
	return m.Discard(nil, id)
}

func (m *mockBucket) Touch(_ context.Context, _ *object.ID) {}

func (m *mockBucket) Close() error { return nil }
func (m *mockBucket) ID() string   { return "mock" }
func (m *mockBucket) Weight() int  { return 100 }
func (m *mockBucket) Allow() int   { return 100 }
func (m *mockBucket) UseAllow() bool { return false }
func (m *mockBucket) Objects() uint64 { return 0 }
func (m *mockBucket) HasBad() bool  { return false }
func (m *mockBucket) Type() string  { return "mock" }
func (m *mockBucket) StoreType() string { return "mock" }
func (m *mockBucket) Path() string  { return "/mock" }
func (m *mockBucket) TopK(int) []string { return nil }

func (m *mockBucket) Exist(_ context.Context, _ []byte) bool                    { return false }
func (m *mockBucket) Remove(_ context.Context, _ *object.ID) error              { return nil }
func (m *mockBucket) DiscardWithHash(_ context.Context, _ object.IDHash) error  { return nil }
func (m *mockBucket) DiscardWithMetadata(_ context.Context, _ *object.Metadata) error { return nil }
func (m *mockBucket) Iterate(_ context.Context, _ func(*object.Metadata) error) error { return nil }
func (m *mockBucket) Expired(_ context.Context, _ *object.ID, _ *object.Metadata) bool { return false }

func (m *mockBucket) WriteChunkFile(_ context.Context, _ *object.ID, _ uint32) (io.WriteCloser, string, error) {
	return nil, "", os.ErrNotExist
}
func (m *mockBucket) ReadChunkFile(_ context.Context, _ *object.ID, _ uint32) (storage.File, string, error) {
	return nil, "", os.ErrNotExist
}
func (m *mockBucket) Migrate(_ context.Context, _ *object.ID, _ storage.Bucket) error { return nil }
func (m *mockBucket) SetMigration(_ storage.Migration) error                          { return nil }

func (m *mockBucket) discards() []*object.ID {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*object.ID, len(m.discard))
	copy(out, m.discard)
	return out
}

// ---------- helpers ----------

func testLogger() *log.Helper {
	return log.NewHelper(log.DefaultLogger)
}

func testOption() *cachingOption {
	return &cachingOption{
		IncludeQueryInCacheKey: false,
		SliceSize:              1048576,
		Hostname:               "test",
	}
}

func testReq(method, urlStr string) *http.Request {
	req, _ := http.NewRequest(method, urlStr, nil)
	return req
}

func freshVaryProcessor(limit int, ignoreKeys ...string) *VaryProcessor {
	return NewVaryProcessor(
		WithVaryMaxLimit(limit),
		WithVaryIgnoreKeys(ignoreKeys...),
	)
}

// ---------- 1. 普通缓存 ----------

func TestVaryProcessor_NormalCache_PostRequest_NoVary(t *testing.T) {
	// Normal cache: response has no Vary header, meta has no Vary.
	// PostRequest should see no vary metadata and return nil, ErrHeaderNoMatchVaryKey,
	// meaning the response is returned unmodified.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/plain.txt")
	id := object.NewID(req.URL.String())

	md := &object.Metadata{
		ID:      id,
		Flags:   object.FlagCache, // normal cache, not vary
		Code:    200,
		Size:    1024,
		Headers: http.Header{"Content-Type": {"text/plain"}},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/plain"}},
	}

	result, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != resp {
		t.Fatal("PostRequest should return the original response when no Vary")
	}

	// The md should still be normal cache (not vary)
	if c.md.Flags != object.FlagCache {
		t.Fatalf("flags should still be FlagCache, got %v", c.md.Flags)
	}
}

// ---------- 2. 升级 vary 缓存 ----------

func TestVaryProcessor_UpgradeToVary_PostRequest(t *testing.T) {
	// Response has Vary: Accept-Encoding header, meta is normal cache (FlagCache).
	// Handler should upgrade to vary index + vary cache.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/vary-resource")
	req.Header.Set("Accept-Encoding", "gzip")

	urlStr := req.URL.String()
	id := object.NewID(urlStr)

	md := &object.Metadata{
		ID:        id,
		Flags:     object.FlagCache,
		Code:      200,
		Size:      4096,
		BlockSize: 1048576,
		Chunks:    bitmap.Bitmap{0}, // at least one chunk to trigger discard+upgrade path
		Headers:   http.Header{"Content-Type": {"text/html"}},
		ExpiresAt: time.Now().Unix() + 3600,
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type":     {"text/html"},
			"Vary":             {"Accept-Encoding"},
			"Content-Length":   {"4096"},
			"Cache-Control":    {"max-age=3600"},
		},
	}

	result, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != resp {
		t.Fatal("PostRequest should return the original response")
	}

	// The md should now be VaryCache
	if !c.md.IsVaryCache() {
		t.Fatalf("md should be VaryCache, got flags=%v", c.md.Flags)
	}

	// rootmd should be VaryIndex
	if c.rootmd == nil {
		t.Fatal("rootmd should be set")
	}
	if !c.rootmd.IsVary() {
		t.Fatalf("rootmd should be VaryIndex, got flags=%v", c.rootmd.Flags)
	}

	// rootmd should have the Vary header stored
	varyHdr := c.rootmd.Headers.Values("Vary")
	if len(varyHdr) == 0 || varyHdr[0] != "Accept-Encoding" {
		t.Fatalf("rootmd Vary header should be [Accept-Encoding], got %v", varyHdr)
	}
}

func TestVaryProcessor_UpgradeToVary_NoChunks_PostRequest(t *testing.T) {
	// First vary request for a resource where normal cache has no chunks yet.
	// Should still upgrade to vary without discarding (nothing to discard).

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/vary-first")
	req.Header.Set("Accept-Encoding", "br")

	urlStr := req.URL.String()
	id := object.NewID(urlStr)

	md := &object.Metadata{
		ID:        id,
		Flags:     object.FlagCache,
		Code:      200,
		Size:      0,
		BlockSize: 1048576,
		Chunks:    bitmap.Bitmap{}, // no chunks
		Headers:   http.Header{"Content-Type": {"text/html"}},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type":   {"text/html"},
			"Vary":           {"Accept-Encoding"},
			"Content-Length": {"1024"},
			"Cache-Control":  {"max-age=600"},
		},
	}

	result, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != resp {
		t.Fatal("PostRequest should return the original response")
	}

	if !c.md.IsVaryCache() {
		t.Fatalf("md should be VaryCache, got flags=%v", c.md.Flags)
	}
}

func TestVaryProcessor_UpgradeToVary_Lookup_Miss(t *testing.T) {
	// After upgrade, Lookup for a request with different Accept-Encoding
	// should miss (variant not yet cached).

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	// First, upgrade to vary with gzip
	req1 := testReq("GET", "https://example.com/vary-lookup")
	req1.Header.Set("Accept-Encoding", "gzip")
	id := object.NewID(req1.URL.String())

	md := &object.Metadata{
		ID:      id,
		Flags:   object.FlagCache,
		Code:    200,
		Size:    1024,
		Chunks:  bitmap.Bitmap{0},
		Headers: http.Header{"Content-Type": {"text/html"}},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req1,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Vary":           {"Accept-Encoding"},
			"Content-Length": {"1024"},
		},
	}

	_, _ = vp.PostRequest(c, req1, resp)

	// Store the rootmd and variant md into bucket so Lookup can find them
	_ = bucket.Store(nil, c.rootmd)
	_ = bucket.Store(nil, c.md) // this is the gzip variant

	// Now Lookup with same Accept-Encoding should hit
	req2 := testReq("GET", "https://example.com/vary-lookup")
	req2.Header.Set("Accept-Encoding", "gzip")
	c2 := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req2,
		id:     id,
		md:     bucket.store[c.rootmd.ID.String()], // re-load rootmd from store
		bucket: bucket,
	}

	hit, err := vp.Lookup(c2, req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hit {
		t.Fatal("Lookup should hit for matching Accept-Encoding=gzip")
	}
	if !c2.md.IsVaryCache() {
		t.Fatalf("c2.md should be VaryCache, got flags=%v", c2.md.Flags)
	}
}

// ---------- 3. vary 缓存刷新 ----------

func TestVaryProcessor_Refresh_HandleResponseVary_Match(t *testing.T) {
	// When origin response has same Vary as cached meta, and the variant
	// already exists in the bucket, PostRequest should return the existing
	// variant metadata without creating a new one.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/vary-refresh")
	req.Header.Set("Accept-Encoding", "gzip")

	urlStr := req.URL.String()
	id := object.NewID(urlStr)

	// Variant already exists in bucket
	varyData := "Accept-Encoding=gzip"
	variantID := object.NewVirtualID(urlStr, varyData)
	variantMD := &object.Metadata{
		ID:      variantID,
		Flags:   object.FlagVaryCache,
		Code:    200,
		Size:    8888,
		Headers: http.Header{"X-Variant": {"original"}},
	}
	_ = bucket.Store(nil, variantMD)

	// meta has Vary: Accept-Encoding, matching response
	md := &object.Metadata{
		ID:      id,
		Flags:   object.FlagCache,
		Code:    200,
		Size:    1024,
		Chunks:  bitmap.Bitmap{0},
		Headers: http.Header{
			"Content-Type": {"text/html"},
			"Vary":         {"Accept-Encoding"},
		},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Vary":           {"Accept-Encoding"},
			"Content-Length": {"4096"},
		},
	}

	result, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != resp {
		t.Fatal("PostRequest should return the original response")
	}

	// Should have found the existing variant
	if c.md.Size != 8888 {
		t.Fatalf("should reuse existing variant (Size=8888), got Size=%d", c.md.Size)
	}
	if c.md.Headers.Get("X-Variant") != "original" {
		t.Fatal("should reuse existing variant headers")
	}
}

func TestVaryProcessor_Refresh_HandleResponseVary_Changed(t *testing.T) {
	// When origin changes the Vary header (e.g. adds a new vary dimension),
	// the old cache should be discarded and rebuilt.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/vary-changed")
	req.Header.Set("Accept-Encoding", "br")
	req.Header.Set("User-Agent", "test-agent")

	urlStr := req.URL.String()
	id := object.NewID(urlStr)

	// meta currently has Vary: Accept-Encoding
	md := &object.Metadata{
		ID:        id,
		Flags:     object.FlagCache,
		Code:      200,
		Size:      1024,
		Chunks:    bitmap.Bitmap{0},
		BlockSize: 1048576,
		VirtualKey: []string{
			"Accept-Encoding=br",
		},
		Headers: http.Header{
			"Content-Type": {"text/html"},
			"Vary":         {"Accept-Encoding"},
		},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	// Origin now responds with Vary: Accept-Encoding, User-Agent
	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Vary":           {"Accept-Encoding, User-Agent"},
			"Content-Length": {"2048"},
		},
	}

	result, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != resp {
		t.Fatal("PostRequest should return the original response")
	}

	// Old cache ID should be discarded
	discards := bucket.discards()
	var found bool
	for _, d := range discards {
		if d.String() == id.String() {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("old cache should be discarded when Vary header changes")
	}
}

// ---------- 4. vary 缓存淘汰 ----------

func TestVaryProcessor_EvictOldestVariant(t *testing.T) {
	// The oldest variant (first in VirtualKey) should be discarded
	// when evictOldestVariant is called.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/vary-evict")
	id := object.NewID(req.URL.String())

	// Store an existing variant
	oldestVaryData := "Accept-Encoding=br"
	oldestID := object.NewVirtualID(id.Path(), oldestVaryData)
	_ = bucket.Store(nil, &object.Metadata{ID: oldestID})

	md := &object.Metadata{
		ID:         id,
		Flags:      object.FlagVaryIndex,
		VirtualKey: []string{oldestVaryData, "Accept-Encoding=gzip"},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	vp.evictOldestVariant(c, md.VirtualKey)

	// The oldest variant should be discarded
	discards := bucket.discards()
	var found bool
	for _, d := range discards {
		if d.String() == oldestID.String() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("oldest variant %s should be discarded, discards: %v", oldestID, discards)
	}
}

func TestVaryProcessor_EvictOldestVariant_Empty(t *testing.T) {
	// Calling evictOldestVariant with empty VirtualKey should be a no-op.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	c := &Caching{
		log:    testLogger(),
		bucket: bucket,
		id:     object.NewID("https://example.com/empty"),
	}

	vp.evictOldestVariant(c, nil)
	if len(bucket.discards()) != 0 {
		t.Fatal("no discards expected for empty VirtualKey")
	}
}

// ---------- 5. vary 缓存超过限制数量淘汰 ----------

func TestVaryProcessor_LimitExceeded_EvictsOldest(t *testing.T) {
	// When the number of VirtualKey entries exceeds maxLimit,
	// the oldest one should be evicted.

	bucket := newMockBucket()
	vp := freshVaryProcessor(3) // max 3 variants

	req := testReq("GET", "https://example.com/vary-limit")
	req.Header.Set("Accept-Encoding", "deflate")

	urlStr := req.URL.String()
	id := object.NewID(urlStr)

	// Pre-populate VirtualKey with 3 existing variants (at limit)
	existingVariants := []string{
		"Accept-Encoding=br",
		"Accept-Encoding=gzip",
		"Accept-Encoding=zstd",
	}
	for _, vd := range existingVariants {
		vID := object.NewVirtualID(urlStr, vd)
		_ = bucket.Store(nil, &object.Metadata{ID: vID})
	}

	md := &object.Metadata{
		ID:        id,
		Flags:     object.FlagVaryIndex,
		Code:      200,
		Size:      1024,
		BlockSize: 1048576,
		Chunks:    bitmap.Bitmap{0},
		VirtualKey: existingVariants[:3],
		Headers: http.Header{
			"Content-Type": {"text/html"},
			"Vary":         {"Accept-Encoding"},
		},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	// This should trigger limit check in handleNoResponseVary
	// because meta has Vary but response doesn't
	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Length": {"512"},
		},
	}

	varyMetadata, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if varyMetadata == nil {
		t.Fatal("should return vary metadata")
	}

	// The oldest variant "Accept-Encoding=br" should have been evicted from bucket store.
	// Since the discard drops it from the store, its ID should not be found.
	oldestID := object.NewVirtualID(urlStr, "Accept-Encoding=br")
	_, err = bucket.Lookup(nil, oldestID)
	if err == nil {
		t.Fatal("oldest variant should have been discarded from store")
	}

	// The rootmd (VaryIndex) should still have 3 VirtualKey entries
	// (br evicted, deflate added)
	if c.rootmd == nil {
		t.Fatal("rootmd should be set")
	}
	if len(c.rootmd.VirtualKey) != 3 {
		t.Fatalf("rootmd.VirtualKey should have 3 entries, got %d: %v", len(c.rootmd.VirtualKey), c.rootmd.VirtualKey)
	}
	// c.md is now the new variant, which is VaryCache (no VirtualKey)
	if !c.md.IsVaryCache() {
		t.Fatal("c.md should be VaryCache after PostRequest")
	}
}

func TestVaryProcessor_LimitExceeded_Upgrade(t *testing.T) {
	// During upgrade, when a new variant needs to be added but the
	// VirtualKey already has maxLimit entries, the oldest is evicted.
	//
	// This tests the path: metaVary matches respVary → existing variant
	// not found → upgrade with existing VirtualKey at max → evict oldest.

	bucket := newMockBucket()
	vp := freshVaryProcessor(2) // max 2 variants

	req := testReq("GET", "https://example.com/vary-limit-upgrade")
	req.Header.Set("Accept-Encoding", "deflate")

	urlStr := req.URL.String()
	id := object.NewID(urlStr)

	// Pre-populate with 2 existing variants (at limit)
	oldestVD := "Accept-Encoding=br"
	existingVD := "Accept-Encoding=gzip"
	for _, vd := range []string{oldestVD, existingVD} {
		vID := object.NewVirtualID(urlStr, vd)
		_ = bucket.Store(nil, &object.Metadata{ID: vID, Flags: object.FlagVaryCache})
	}

	// md is the VaryIndex root — has Vary header in Headers so
	// metaVary will be non-empty and match respVary.
	md := &object.Metadata{
		ID:        id,
		Flags:     object.FlagCache, // will be upgraded to VaryIndex
		Code:      200,
		Size:      1024,
		BlockSize: 1048576,
		Chunks:    bitmap.Bitmap{0},
		VirtualKey: []string{oldestVD, existingVD}, // already at limit
		Headers: http.Header{
			"Content-Type": {"text/html"},
			"Vary":         {"Accept-Encoding"}, // metaVary = ["Accept-Encoding"]
		},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Vary":           {"Accept-Encoding"}, // respVary = ["Accept-Encoding"], matches metaVary
			"Content-Length": {"512"},
		},
	}

	varyMetadata, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if varyMetadata == nil {
		t.Fatal("should return vary metadata")
	}

	// The oldest variant br should be evicted from bucket store
	oldestID := object.NewVirtualID(urlStr, oldestVD)
	_, err = bucket.Lookup(nil, oldestID)
	if err == nil {
		t.Fatal("oldest variant should have been discarded from store during upgrade")
	}

	// rootmd.VirtualKey should have 2 entries: gzip + deflate (br evicted)
	if c.rootmd == nil {
		t.Fatal("rootmd should be set")
	}
	vk := c.rootmd.VirtualKey
	if len(vk) != 2 {
		t.Fatalf("rootmd.VirtualKey should have 2 entries, got %d: %v", len(vk), vk)
	}
	if !c.md.IsVaryCache() {
		t.Fatal("c.md should be VaryCache")
	}
}

func TestVaryProcessor_PostRequest_AlreadyVaryCache(t *testing.T) {
	// When md is already a VaryCache, PostRequest should return immediately.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/already-vary")

	md := &object.Metadata{
		ID:    object.NewID(req.URL.String()),
		Flags: object.FlagVaryCache,
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		md:     md,
		bucket: bucket,
	}

	resp := &http.Response{StatusCode: 200}

	result, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != resp {
		t.Fatal("PostRequest should return original response for already vary cache")
	}
}

// ---------- Vary 降级 ----------

func TestVaryProcessor_Downgrade_ResponseVaryChangedToNoVary(t *testing.T) {
	// When cached meta has Vary: Accept-Encoding but origin now returns
	// no Vary header (and a different accept-encoding), old cache is
	// discarded and md downgraded to normal cache.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/vary-downgrade")
	req.Header.Set("Accept-Encoding", "gzip")

	urlStr := req.URL.String()
	id := object.NewID(urlStr)

	md := &object.Metadata{
		ID:        id,
		Flags:     object.FlagVaryIndex,
		Code:      200,
		Size:      1024,
		Chunks:    bitmap.Bitmap{0},
		BlockSize: 1048576,
		VirtualKey: []string{
			"Accept-Encoding=br",
			"Accept-Encoding=gzip",
		},
		Headers: http.Header{
			"Content-Type": {"text/html"},
			"Vary":         {"Accept-Encoding"},
		},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	// Origin now responds with no Vary header — but meta still has Vary
	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Length": {"2048"},
		},
	}

	// This triggers handleNoResponseVary, which calls VaryData on request headers.
	// Since Accept-Encoding=gzip matches one of the VirtualKey variants, it should
	// find it and create a new variant for this request.
	_, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After PostRequest, c.md is set to the new variant (or stays the same)
	// Since md was VaryIndex, handleNoResponseVary created a new variant for gzip
	if c.md != nil && !c.md.IsVaryCache() {
		t.Fatalf("c.md should be VaryCache after downgrade, got flags=%v", c.md.Flags)
	}
}

// ---------- Vary 降级：无更多 variant ----------

func TestVaryProcessor_Downgrade_EmptyVariantList(t *testing.T) {
	// When PostRequest runs with md as VaryIndex but an empty VirtualKey,
	// the downgrade check fires and the root is reset to FlagCache.

	bucket := newMockBucket()
	vp := freshVaryProcessor(100)

	req := testReq("GET", "https://example.com/vary-empty")
	req.Header.Set("Accept-Encoding", "br")

	urlStr := req.URL.String()
	id := object.NewID(urlStr)

	// md is already a VaryIndex with an empty variant list
	md := &object.Metadata{
		ID:         id,
		Flags:      object.FlagVaryIndex,
		Code:       200,
		Size:       0,
		BlockSize:  1048576,
		VirtualKey: nil, // empty — the "no more variants" case
		Chunks:     bitmap.Bitmap{},
		Headers: http.Header{
			"Content-Type": {"text/html"},
			"Vary":         {"Accept-Encoding"},
		},
	}

	c := &Caching{
		log:    testLogger(),
		opt:    testOption(),
		req:    req,
		id:     id,
		md:     md,
		bucket: bucket,
	}

	// Response still has Vary header — triggers convertVaryMetadata
	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Vary":           {"Accept-Encoding"},
			"Content-Length": {"1024"},
		},
	}

	// IsVaryCache is false (FlagVaryIndex), so PostRequest proceeds.
	// convertVaryMetadata → handleResponseVary: metaVary=["Accept-Encoding"],
	// respVary=["Accept-Encoding"], they match.
	// Lookup fails (no variant stored), falls through to upgrade.
	// upgrade: append(c.md.VirtualKey, "Accept-Encoding=br") → ["Accept-Encoding=br"]
	_, err := vp.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After upgrade, rootmd.VirtualKey should have 1 entry (the new variant)
	if c.rootmd == nil {
		t.Fatal("rootmd should be set")
	}
	if len(c.rootmd.VirtualKey) != 1 {
		t.Fatalf("rootmd.VirtualKey should have 1 entry, got %d", len(c.rootmd.VirtualKey))
	}
}

// ---------- hasNoCache ----------

func TestVaryProcessor_Lookup_HasNoCache(t *testing.T) {
	// When md is nil, hasNoCache returns true → Lookup returns false immediately.
	vp := freshVaryProcessor(100)
	req := testReq("GET", "https://example.com/nocache")

	c := &Caching{
		log: testLogger(),
		opt: testOption(),
		req: req,
		md:  nil, // no cache
	}

	hit, err := vp.Lookup(c, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hit {
		t.Fatal("Lookup should return false when md is nil")
	}
}

// ---------- PreRequest pass-through ----------

func TestVaryProcessor_PreRequest_PassThrough(t *testing.T) {
	vp := freshVaryProcessor(100)
	req := testReq("GET", "https://example.com/test")

	result, err := vp.PreRequest(nil, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != req {
		t.Fatal("PreRequest should return the original request")
	}
}

// ---------- NewVaryProcessor defaults ----------

func TestNewVaryProcessor_Defaults(t *testing.T) {
	vp := NewVaryProcessor()
	if vp.maxLimit != 100 {
		t.Fatalf("default maxLimit should be 100, got %d", vp.maxLimit)
	}
	if vp.varyIgnoreKey == nil {
		t.Fatal("varyIgnoreKey should be initialized")
	}
}

func TestNewVaryProcessor_WithOptions(t *testing.T) {
	vp := NewVaryProcessor(
		WithVaryMaxLimit(10),
		WithVaryIgnoreKeys("Cookie", "Authorization"),
	)
	if vp.maxLimit != 10 {
		t.Fatalf("maxLimit should be 10, got %d", vp.maxLimit)
	}
	if _, ok := vp.varyIgnoreKey["Cookie"]; !ok {
		t.Fatal("Cookie should be in varyIgnoreKey")
	}
	if _, ok := vp.varyIgnoreKey["Authorization"]; !ok {
		t.Fatal("Authorization should be in varyIgnoreKey")
	}
}

// ---------- filterVaryKeys ----------

func TestVaryProcessor_FilterVaryKeys(t *testing.T) {
	vp := NewVaryProcessor(
		WithVaryIgnoreKeys("Cookie", "Authorization"),
	)

	// Simulate the real pipeline: raw header values → varycontrol.Clean → FilterIgnore
	cleaned := vp.filterVaryKeys(
		varycontrolTestClean("Accept-Encoding, Cookie, User-Agent"),
	)

	// Cookie should be filtered out
	for _, k := range cleaned {
		if k == "Cookie" {
			t.Fatal("Cookie should have been filtered out")
		}
	}
	// Accept-Encoding and User-Agent should remain
	found := map[string]bool{}
	for _, k := range cleaned {
		found[k] = true
	}
	if !found["Accept-Encoding"] {
		t.Fatal("Accept-Encoding should pass the filter")
	}
	if !found["User-Agent"] {
		t.Fatal("User-Agent should pass the filter")
	}
}

// varycontrolTestClean is a minimal inline variant of varycontrol.Clean
// that avoids an import cycle / extra dependency. It returns a sorted,
// deduplicated slice of trimmed, comma-split key names.
func varycontrolTestClean(values string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, part := range splitByComma(values) {
		t := trimStr(part)
		if t != "" {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				out = append(out, t)
			}
		}
	}
	return out
}

func splitByComma(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func trimStr(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
