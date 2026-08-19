package caching

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kelindar/bitmap"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
	"github.com/omalloc/tavern/storage/bucket/memory"
	"github.com/omalloc/tavern/storage/sharedkv"
)

func newVaryTestCaching(t *testing.T, req *http.Request) *Caching {
	t.Helper()

	bucket, err := memory.New(&storagev1.BucketConfig{}, sharedkv.NewEmpty())
	if err != nil {
		t.Fatalf("create memory bucket: %v", err)
	}
	t.Cleanup(func() { _ = bucket.Close() })

	rootID, err := newObjectIDFromRequest(req, "", true)
	if err != nil {
		t.Fatalf("create root ID: %v", err)
	}
	now := time.Now().Unix()
	return &Caching{
		log:       log.NewHelper(log.GetLogger()),
		req:       req,
		ctx:       req.Context(),
		rootID:    rootID,
		id:        rootID,
		bucket:    bucket,
		cacheable: true,
		opt: &cachingOption{
			IncludeQueryInCacheKey: true,
			SliceSize:              1024,
			VaryLimit:              100,
		},
		md: &object.Metadata{
			ID:          rootID,
			BlockSize:   1024,
			Size:        16,
			Code:        http.StatusOK,
			RespUnix:    now,
			LastRefUnix: now,
			ExpiresAt:   now + 60,
			Headers:     make(http.Header),
			Chunks:      bitmap.Bitmap{},
			Parts:       bitmap.Bitmap{},
		},
	}
}

func TestVaryPostRequestStoresCommaValueAsOneVariant(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/object", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	c := newVaryTestCaching(t, req)

	processor := NewVaryProcessor()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": {"Accept-Encoding"}}}
	_, err := processor.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("PostRequest() error = %v", err)
	}

	if c.rootmd == nil || len(c.rootmd.VirtualKey) != 1 {
		t.Fatalf("root variants = %#v, want one opaque variant key", c.rootmd)
	}
	if !strings.HasPrefix(c.rootmd.VirtualKey[0], "v2:") {
		t.Fatalf("variant key = %q, want v2 key", c.rootmd.VirtualKey[0])
	}
	if !c.md.IsVaryCache() {
		t.Fatalf("metadata flag = %s, want VARY_CACHE", c.md.Flags)
	}
}

func TestVaryIgnoreKeyPreventsVariantIndex(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/object", nil)
	req.Header.Set("Cookie", "session=one")
	c := newVaryTestCaching(t, req)

	processor := NewVaryProcessor(WithVaryIgnoreKeys("cookie"))
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": {"Cookie"}}}
	_, err := processor.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("PostRequest() error = %v", err)
	}
	if c.rootmd != nil || c.md.IsVaryCache() {
		t.Fatalf("ignored Vary field created a variant index: root=%v flags=%s", c.rootmd, c.md.Flags)
	}
}

func TestVaryIgnoreKeyInvalidatesExistingIndex(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/object", nil)
	c := newVaryTestCaching(t, req)
	c.md.Flags = object.FlagVaryIndex
	c.md.Size = 0
	c.md.Headers.Set("Vary", "Cookie")
	c.md.VirtualKey = []string{"legacy-cookie-variant"}
	if err := c.bucket.Store(t.Context(), c.md); err != nil {
		t.Fatalf("store root metadata: %v", err)
	}

	hit, err := NewVaryProcessor(WithVaryIgnoreKeys("COOKIE")).Lookup(c, req)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if hit || c.md != nil {
		t.Fatalf("fully ignored index hit=%t metadata=%v", hit, c.md)
	}
	if md, lookupErr := c.bucket.Lookup(t.Context(), c.rootID); md != nil || !errors.Is(lookupErr, storagev1.ErrKeyNotFound) {
		t.Fatalf("ignored root still exists: metadata=%v error=%v", md, lookupErr)
	}
}

func TestVaryWildcardDisablesAndDiscardsCache(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/object", nil)
	c := newVaryTestCaching(t, req)
	if err := c.bucket.Store(t.Context(), c.md); err != nil {
		t.Fatalf("store root metadata: %v", err)
	}

	processor := NewVaryProcessor()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": {"*"}}}
	_, err := processor.PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("PostRequest() error = %v", err)
	}
	if c.cacheable {
		t.Fatal("Vary wildcard response remained cacheable")
	}
	if md, lookupErr := c.bucket.Lookup(t.Context(), c.rootID); md != nil || !errors.Is(lookupErr, storagev1.ErrKeyNotFound) {
		t.Fatalf("wildcard root still exists: metadata=%v error=%v", md, lookupErr)
	}
}

func TestVaryLookupFallsBackToLegacyKey(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/object", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	c := newVaryTestCaching(t, req)

	root := c.md
	root.Flags = object.FlagVaryIndex
	root.Size = 0
	root.Headers.Set("Vary", "accept-encoding")
	legacyKey := varycontrol.LegacyClean(root.Headers.Values("Vary")...)
	legacyData := legacyKey.LegacyVaryData(req.Header)
	legacyID, _ := newObjectIDFromRequest(req, legacyData, true)
	root.VirtualKey = []string{legacyData}
	variant := root.Clone()
	variant.ID = legacyID
	variant.Flags = object.FlagVaryCache
	variant.Size = 16
	if err := c.bucket.Store(t.Context(), root); err != nil {
		t.Fatalf("store root: %v", err)
	}
	if err := c.bucket.Store(t.Context(), variant); err != nil {
		t.Fatalf("store legacy variant: %v", err)
	}

	hit, err := NewVaryProcessor().Lookup(c, req)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !hit || c.id.HashStr() != legacyID.HashStr() {
		t.Fatalf("legacy lookup hit=%t id=%s, want %s", hit, c.id.HashStr(), legacyID.HashStr())
	}
}

func TestVaryMissSelectsVariantIDBeforeRequestCollapse(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/object", nil)
	req.Header.Set("Accept-Encoding", "br")
	c := newVaryTestCaching(t, req)
	c.md.Flags = object.FlagVaryIndex
	c.md.Size = 0
	c.md.Headers.Set("Vary", "Accept-Encoding")

	hit, err := NewVaryProcessor().Lookup(c, req)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if hit {
		t.Fatal("unexpected Vary hit")
	}
	if c.id.HashStr() == c.rootID.HashStr() || !strings.HasPrefix(c.id.Ext(), "v2:") {
		t.Fatalf("miss ID = %s, want a v2 variant ID distinct from root", c.id.String())
	}
}

func TestVaryRemovalDowngradesToNormalCache(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/object", nil)
	req.Header.Set("User-Agent", "agent-a")
	c := newVaryTestCaching(t, req)

	varyKey := varycontrol.Clean("User-Agent")
	varyData := varyKey.VaryData(req.Header)
	variantID, _ := newObjectIDFromRequest(req, varyData, true)
	root := c.md.Clone()
	root.Flags = object.FlagVaryIndex
	root.Size = 0
	root.Headers = http.Header{"Vary": {"User-Agent"}}
	root.VirtualKey = []string{varyData}
	variant := c.md.Clone()
	variant.ID = variantID
	variant.Flags = object.FlagVaryCache
	variant.Headers.Set("Vary", "User-Agent")
	if err := c.bucket.Store(t.Context(), root); err != nil {
		t.Fatalf("store root: %v", err)
	}
	if err := c.bucket.Store(t.Context(), variant); err != nil {
		t.Fatalf("store variant: %v", err)
	}
	c.rootmd = root
	c.md = variant
	c.id = variantID

	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
	_, err := NewVaryProcessor().PostRequest(c, req, resp)
	if err != nil {
		t.Fatalf("PostRequest() error = %v", err)
	}
	if c.rootmd != nil || c.md.Flags != object.FlagCache || c.id.HashStr() != c.rootID.HashStr() {
		t.Fatalf("cache was not downgraded: root=%v flags=%s id=%s", c.rootmd, c.md.Flags, c.id.HashStr())
	}
}

func TestStoreRootMetadataMergesConcurrentVariants(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/object", nil)
	first := newVaryTestCaching(t, req)
	second := &Caching{
		log:       first.log,
		req:       first.req,
		ctx:       first.ctx,
		rootID:    first.rootID,
		id:        first.id,
		bucket:    first.bucket,
		cacheable: true,
		opt:       first.opt,
	}

	newRoot := func(key string) *object.Metadata {
		root := first.md.Clone()
		root.ID = first.rootID
		root.Flags = object.FlagVaryIndex
		root.Size = 0
		root.Headers = http.Header{"Vary": {"User-Agent"}}
		root.VirtualKey = []string{key}
		return root
	}
	newVariant := func(key string) *object.Metadata {
		variant := first.md.Clone()
		variant.ID = object.NewVirtualID(first.rootID.Path(), key)
		variant.Flags = object.FlagVaryCache
		return variant
	}

	first.rootmd = newRoot("variant-a")
	first.md = newVariant("variant-a")
	second.rootmd = newRoot("variant-b")
	second.md = newVariant("variant-b")
	if err := first.storeRootMetadata(); err != nil {
		t.Fatalf("store first root: %v", err)
	}
	if err := second.storeRootMetadata(); err != nil {
		t.Fatalf("store second root: %v", err)
	}

	root, err := first.bucket.Lookup(t.Context(), first.rootID)
	if err != nil {
		t.Fatalf("lookup merged root: %v", err)
	}
	if len(root.VirtualKey) != 2 || !slices.Contains(root.VirtualKey, "variant-a") || !slices.Contains(root.VirtualKey, "variant-b") {
		t.Fatalf("merged variants = %#v", root.VirtualKey)
	}
}

func TestAppendVariantKeyEnforcesLimitAfterDeduplication(t *testing.T) {
	keys, err := appendVariantKey([]string{"same", "same"}, "same", 1)
	if err != nil || len(keys) != 1 {
		t.Fatalf("deduplicate existing key: keys=%#v error=%v", keys, err)
	}
	if _, err := appendVariantKey(keys, "new", 1); !errors.Is(err, ErrVarySizeLimited) {
		t.Fatalf("limit error = %v, want %v", err, ErrVarySizeLimited)
	}
}
