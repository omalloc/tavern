package caching

import (
	"bytes"
	"compress/gzip"
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

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
)

const (
	acceptanceWebP = `image/webp;profile="a,b", image/jpeg;q=0.8`
	acceptanceJPEG = "image/jpeg, image/webp;q=0.1"
)

func acceptancePayload(t *testing.T, mime, encoding string) []byte {
	t.Helper()
	// Distinct opaque origin payloads exercise transparent byte forwarding;
	// image decoding/encoding belongs to the P3 worker contract.
	body := []byte(mime + ":" + strings.Repeat("origin-bytes-", 15))
	if encoding != "gzip" {
		return body
	}
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func writeAcceptanceResponse(w http.ResponseWriter, mime, encoding string, fields []string, body []byte) {
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "max-age=600")
	if encoding == "gzip" {
		w.Header().Set("Content-Encoding", encoding)
	}
	for _, field := range fields {
		w.Header().Add("Vary", field)
	}
	w.Write(body)
}

func TestVaryAcceptanceEncodingCartesian(t *testing.T) {
	type choice struct {
		accept, mime, encoding string
		wire                   []byte
	}
	var choices []choice
	for _, format := range []struct{ accept, mime string }{{acceptanceWebP, "image/webp"}, {acceptanceJPEG, "image/jpeg"}} {
		for _, encoding := range []string{"identity", "gzip"} {
			choices = append(choices, choice{format.accept, format.mime, encoding, acceptancePayload(t, format.mime, encoding)})
		}
	}
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for _, c := range choices {
			if c.accept == r.Header.Get("Accept") && c.encoding == r.Header.Get("Accept-Encoding") {
				writeAcceptanceResponse(w, c.mime, c.encoding, []string{"Accept", "Accept-Encoding"}, c.wire)
				return
			}
		}
		http.Error(w, "unknown test selection", 400)
	})
	for round := range 2 {
		for _, c := range choices {
			resp, body := f.get(t, http.Header{"Accept": {c.accept}, "Accept-Encoding": {c.encoding}})
			if resp.Header.Get("Content-Type") != c.mime || !bytes.Equal([]byte(body), c.wire) || resp.ContentLength != int64(len(c.wire)) {
				t.Fatalf("crossed %s/%s: %v, %q", c.mime, c.encoding, resp.Header, body)
			}
			wantEncoding := ""
			if c.encoding == "gzip" {
				wantEncoding = "gzip"
			}
			if resp.Header.Get("Content-Encoding") != wantEncoding {
				t.Fatal("changed representation encoding", resp.Header)
			}
			if round == 1 && !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "HIT") {
				t.Fatal("second request did not hit", resp.Header)
			}
			if c.encoding == "gzip" {
				reader, err := gzip.NewReader(strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := io.ReadAll(reader)
				reader.Close()
				if err != nil || !bytes.Equal(decoded, acceptancePayload(t, c.mime, "identity")) {
					t.Fatal("invalid cached gzip bytes", err)
				}
			}
		}
	}
	if calls.Load() != 4 || len(f.root(t).Resource.Variants) != 4 {
		t.Fatal("Cartesian selections did not hit independently", calls.Load())
	}
}

func TestVaryAcceptanceFieldSetsAndRevalidation(t *testing.T) {
	t.Run("200 field evolution", func(t *testing.T) {
		fields := [][]string{nil, {"Accept"}, {"Accept", "Accept-Encoding"}, nil}
		var phase, calls atomic.Int32
		f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if phase.Load() > 0 && r.Header.Get("If-None-Match") == "" {
				t.Error("expired representation was not validated")
			}
			w.Header().Set("ETag", fmt.Sprintf(`"phase-%d"`, phase.Load()))
			writeAcceptanceResponse(w, "image/jpeg", "identity", fields[phase.Load()], []byte(fmt.Sprint("body-", phase.Load())))
		})
		header := http.Header{"Accept": {acceptanceJPEG}, "Accept-Encoding": {"identity"}}
		var previous object.IDHash
		for i, want := range fields {
			phase.Store(int32(i))
			if i > 0 {
				f.clock.Add(601)
			}
			for range 2 {
				_, body := f.get(t, header)
				if body != fmt.Sprint("body-", i) {
					t.Fatal(body)
				}
			}
			root := f.root(t)
			if len(root.Headers) != 0 || len(root.Resource.Variants) != 1 {
				t.Fatal("response fields leaked into root or selected reference was retained")
			}
			ref := root.Resource.Variants[0]
			if len(ref.VaryFields) != len(want) || i > 0 && ref.RepresentationID.Hash() == previous {
				t.Fatal("200 inherited old selection/body identity")
			}
			previous = ref.RepresentationID.Hash()
		}
		if calls.Load() != 4 {
			t.Fatal("200 index updates were skipped", calls.Load())
		}
	})
	t.Run("different field sets coexist", func(t *testing.T) {
		jpeg := acceptancePayload(t, "image/jpeg", "identity")
		webp := acceptancePayload(t, "image/webp", "identity")
		compressed := acceptancePayload(t, "image/webp", "gzip")
		var calls atomic.Int32
		f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Header.Get("Accept") == acceptanceJPEG {
				writeAcceptanceResponse(w, "image/jpeg", "identity", []string{"Accept"}, jpeg)
				return
			}
			body, encoding := webp, "identity"
			if r.Header.Get("Accept-Encoding") == "gzip" {
				body, encoding = compressed, "gzip"
			}
			writeAcceptanceResponse(w, "image/webp", encoding, []string{"Accept", "Accept-Encoding"}, body)
		})
		for round := range 2 {
			for _, tc := range []struct {
				accept, encoding string
				body             []byte
			}{
				{acceptanceJPEG, "identity", jpeg}, {acceptanceJPEG, "gzip", jpeg}, {acceptanceWebP, "identity", webp}, {acceptanceWebP, "gzip", compressed},
			} {
				resp, body := f.get(t, http.Header{"Accept": {tc.accept}, "Accept-Encoding": {tc.encoding}})
				if !bytes.Equal([]byte(body), tc.body) {
					t.Fatal("root-wide union changed selector semantics")
				}
				if round == 1 && !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "HIT") {
					t.Fatal("second request did not reuse its own field set", resp.Header)
				}
			}
		}
		if calls.Load() != 3 || len(f.root(t).Resource.Variants) != 3 {
			t.Fatal("per-reference field sets were not preserved", calls.Load())
		}
	})
	t.Run("304 replaces logical selector", func(t *testing.T) {
		var calls atomic.Int32
		f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				originImage(w, "unchanged bytes", "Accept", `"v1"`)
				return
			}
			if r.Header.Get("If-None-Match") != `"v1"` {
				t.Error("missing validation condition")
			}
			w.Header().Set("Vary", "Accept, Accept-Encoding")
			w.Header().Set("Age", "0")
			w.WriteHeader(304)
		})
		header := http.Header{"Accept": {acceptanceJPEG}, "Accept-Encoding": {"identity"}}
		f.get(t, header)
		before := f.root(t).Resource.Variants[0]
		f.clock.Add(601)
		f.get(t, header)
		f.get(t, header)
		after := f.root(t).Resource.Variants[0]
		if calls.Load() != 2 || len(after.VaryFields) != 2 || before.SelectorFingerprint == after.SelectorFingerprint || before.Revision == after.Revision || before.RepresentationID.Hash() != after.RepresentationID.Hash() {
			t.Fatal("304 skipped index replacement or rewrote body")
		}
		if _, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {acceptanceJPEG}, "Accept-Encoding": {"gzip"}}); !errors.Is(err, storage.ErrKeyNotFound) {
			t.Fatal("304 reused another encoding", err)
		}
	})
}

func TestVaryAcceptanceCommaSelectorRetirement(t *testing.T) {
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		originImage(w, r.Header.Get("Accept"), "Accept", "")
	})
	for _, accept := range []string{acceptanceWebP, acceptanceJPEG} {
		f.get(t, http.Header{"Accept": {accept}})
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {acceptanceWebP}})
	if err != nil {
		t.Fatal(err)
	}
	retired, err := f.repo.Retire(t.Context(), f.key, selected.Reference)
	if err != nil || retired.ID.Hash() != selected.Representation.ID.Hash() || retired.Location != f.bucket.ID() {
		t.Fatal("comma selector reconstructed a phantom key", retired, err)
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {acceptanceWebP}}); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("reference remained readable after retirement", err)
	}
	// Prior response readers have all closed. Only now is physical cleanup safe.
	if err := f.bucket.Discard(t.Context(), retired.ID); err != nil {
		t.Fatal(err)
	}
	selected.Representation.Chunks.Range(func(index uint32) {
		file, _, err := f.bucket.ReadChunkFile(t.Context(), retired.ID, index)
		if file != nil {
			file.Close()
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("real child chunk %d was not removed: %v", index, err)
		}
	})
	resp, body := f.get(t, http.Header{"Accept": {acceptanceJPEG}})
	if body != acceptanceJPEG || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "HIT") || calls.Load() != 2 {
		t.Fatal("cleanup damaged the other comma selector", resp.Header, body)
	}
	f.get(t, http.Header{"Accept": {acceptanceWebP}})
	if calls.Load() != 3 || len(f.root(t).Resource.Variants) != 2 {
		t.Fatal("retired choice did not refill independently", calls.Load())
	}
}

func TestVaryAcceptanceConcurrentBudget(t *testing.T) {
	const variants = 4
	entered, release := make(chan struct{}, variants), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Variant") != "overflow" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		originImage(w, r.Header.Get("X-Variant"), "X-Variant", "")
	})
	f.opt.VaryLimit = variants
	type result struct {
		body, want string
		err        error
	}
	results := make(chan result, variants)
	for i := range variants {
		value := fmt.Sprint("variant-", i)
		req := varyRequest(t, f.target, http.Header{"X-Variant": {value}})
		go func() { _, body, err := readVaryResponse(f.rt, req); results <- result{body, value, err} }()
	}
	for range variants {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("requests did not overlap")
		}
	}
	unblock()
	for range variants {
		r := <-results
		if r.err != nil || r.body != r.want {
			t.Fatal(r)
		}
	}
	if len(f.root(t).Resource.Variants) != variants {
		t.Fatal("concurrent publication lost a reference")
	}
	for range 2 {
		resp, body := f.get(t, http.Header{"X-Variant": {"overflow"}})
		if body != "overflow" || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "BYPASS") {
			t.Fatal(resp.Header, body)
		}
	}
	for i := range variants {
		value := fmt.Sprint("variant-", i)
		if _, body := f.get(t, http.Header{"X-Variant": {value}}); body != value {
			t.Fatal(body)
		}
	}
	if calls.Load() != variants+2 || len(f.root(t).Resource.Variants) != variants {
		t.Fatal("overflow deleted committed choices", calls.Load())
	}
}

func TestVaryAcceptancePublicConfigurationCannotEnableV2(t *testing.T) {
	var calls atomic.Int32
	_, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		originImage(w, r.Header.Get("Accept"), "Accept", "")
	})
	mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{
		"include_query_in_cache_key": true, "slice_size": 64, "vary_limit": 16,
		"representation": map[string]any{"enabled": true, "writes": true, "namespace": "http-v2", "isolation_scope": "origin"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	rt := mw(http.DefaultTransport)
	for range 2 {
		resp, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {acceptanceJPEG}}))
		if err != nil || body != acceptanceJPEG || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "BYPASS") {
			t.Fatal("public configuration enabled the controlled path", resp, body, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("public Vary flow started caching")
	}
	requireEmptyVaryCache(t, bucket)
	key, err := object.NewResourceKey(target, "http-v2", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bucket.(storage.ControlStore).LoadControl(t.Context(), key.ID()); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("public configuration created a v2 control record", err)
	}
}
