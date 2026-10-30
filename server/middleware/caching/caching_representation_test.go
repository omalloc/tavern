package caching

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/server/middleware"
	"github.com/omalloc/tavern/storage/representation"
)

type representationTestStorage struct {
	storage.Storage
	bucket storage.Bucket
}

func (s *representationTestStorage) Select(context.Context, *object.ID) storage.Bucket {
	return s.bucket
}
func (s *representationTestStorage) Buckets() []storage.Bucket { return []storage.Bucket{s.bucket} }

// Bind the private read-only integration without creating a public v2 switch.
func representationTestTransport(store storage.Storage, opt *cachingOption) http.RoundTripper {
	pc := NewProcessorChain(NewStateProcessor(), NewVaryProcessor(), NewRevalidateProcessor())
	return middleware.RoundTripperFunc(func(req *http.Request) (resp *http.Response, err error) {
		c, err := pc.preCacheProcessor(proxy.GetProxy(), store, opt, req)
		defer func() {
			if err != nil {
				closeBody(resp)
				c.finishRepresentationLease(nil)
			} else {
				c.finishRepresentationLease(resp)
			}
		}()
		if err != nil {
			return nil, err
		}
		if c.hit {
			return c.respondFromCache(req)
		}
		resp, err = c.doProxy(req, false)
		if err != nil {
			return resp, err
		}
		return pc.postCacheProcessor(c, req, resp)
	})
}

func TestRepresentationLookupKeepsResourceAndResponseSeparate(t *testing.T) {
	var origins atomic.Int32
	_, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		origins.Add(1)
		w.Header().Set("Content-Type", r.Header.Get("Accept"))
		w.Header().Set("Vary", "Accept")
		w.Header().Set("Cache-Control", "max-age=600")
		fmt.Fprint(w, "origin image")
	})
	store := &representationTestStorage{bucket: bucket}
	key, err := object.NewResourceKey(target, "http-v2", "origin-a")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := representation.NewIndexRepository(bucket.(storage.ControlStore), bucket.ID(), func(ctx context.Context, location string, id *object.ID) (*object.Metadata, error) {
		if location != bucket.ID() {
			t.Fatal("unstable data location")
		}
		return bucket.Lookup(ctx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := repo.Update(t.Context(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"Accept": {"image/jpeg,image/webp;q=0.8"}}
	ref, err := representation.NewHTTPSelector([]string{"Accept"}, header)
	if err != nil {
		t.Fatal(err)
	}
	id, err := key.RepresentationID(root.Resource.Incarnation, 0, ref.SelectorFingerprint, "body-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	ref.RepresentationID, ref.ResponseDate = id, now
	seedVaryTestObject(t, bucket, &object.Metadata{ID: id, RecordKind: object.RecordRepresentation, Code: 200, Size: 10, BlockSize: 64,
		RespUnix: now, ExpiresAt: now + 600,
		Headers: http.Header{"Vary": {"Accept"}, "Content-Length": {"10"}, "Content-Type": {"image/jpeg"}, "Cache-Control": {"max-age=600"}, "Age": {"120"}, "Date": {time.Unix(now, 0).UTC().Format(http.TimeFormat)}},
		Representation: &object.RepresentationInfo{SchemaVersion: object.RepresentationSchemaVersion,
			ResourceID: key.ResourceID(), Incarnation: root.Resource.Incarnation, BodyGeneration: "body-1",
			State: object.RepresentationComplete, Kind: object.RepresentationOrigin},
	}, "jpeg bytes")
	root, err = repo.Update(t.Context(), key, func(record *object.ResourceRecord) error { record.Variants = append(record.Variants, ref); return nil })
	if err != nil {
		t.Fatal(err)
	}
	opt := &cachingOption{SliceSize: 64, IncludeQueryInCacheKey: true, Hostname: "test", representation: &representationLookup{
		repository: repo, namespace: "http-v2", isolationScope: "origin-a",
	}}
	rt := representationTestTransport(store, opt)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := varyRequest(t, target, header)
		req.Method = method
		resp, body, err := readVaryResponse(rt, req)
		if err != nil {
			t.Fatal(err)
		}
		if method == http.MethodGet && body != "jpeg bytes" || method == http.MethodHead && body != "" {
			t.Fatalf("%s: %q", method, body)
		}
		age, err := strconv.ParseInt(resp.Header.Get("Age"), 10, 64)
		if err != nil || age < 120 || resp.Header.Get("Date") != time.Unix(now, 0).UTC().Format(http.TimeFormat) {
			t.Fatal("lost origin Date/Age", resp.Header)
		}
	}
	rangeHeader := header.Clone()
	rangeHeader.Set("Range", "bytes=1-3")
	resp, body, err := readVaryResponse(rt, varyRequest(t, target, rangeHeader))
	if err != nil || resp.StatusCode != 206 || body != "peg" || resp.Header.Get("Content-Range") != "bytes 1-3/10" {
		t.Fatalf("range: %v, %q, %v", resp, body, err)
	}
	if origins.Load() != 0 {
		t.Fatal("ready representation fetched origin")
	}
	pc := NewProcessorChain(NewStateProcessor(), NewVaryProcessor(), NewRevalidateProcessor())
	cached, err := pc.preCacheProcessor(proxy.GetProxy(), store, opt, varyRequest(t, target, header))
	if err != nil || !cached.hit || cached.resourceMD == nil || cached.variantRef == nil || cached.rootmd != nil {
		t.Fatalf("missing independent references: %+v, %v", cached, err)
	}
	cached.md = cached.md.Clone()
	cached.md.Headers.Set("Vary", "X-Other")
	cached.variantRef.SelectorSnapshot.Fields[0].Values[0] = "changed"
	if len(cached.resourceMD.Headers) != 0 || cached.resourceMD.Resource.Variants[0].SelectorSnapshot.Fields[0].Values[0] != header.Get("Accept") {
		t.Fatal("changing response/reference changed the root")
	}
	// A miss uses the original request and never opens the legacy writer or
	// publishes a HEAD body descriptor into this namespace.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := varyRequest(t, target, http.Header{"Accept": {"image/png"}})
		req.Method = method
		resp, _, err := readVaryResponse(rt, req)
		if err != nil || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "BYPASS") {
			t.Fatalf("miss: %v, %v", resp, err)
		}
	}
	after, err := repo.Load(t.Context(), key)
	if err != nil || after.Resource.Revision != root.Resource.Revision || len(after.Resource.Variants) != 1 || len(after.Headers) != 0 {
		t.Fatal("miss or response assembly modified root", err)
	}
	if origins.Load() != 2 {
		t.Fatalf("origin calls = %d", origins.Load())
	}
	if _, err := repo.Update(t.Context(), key, func(record *object.ResourceRecord) error { record.RevalidateEpoch++; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, body, err := readVaryResponse(rt, varyRequest(t, target, header)); err != nil || body != "origin image" {
		t.Fatalf("unvalidated epoch served old response: %q, %v", body, err)
	}
}

type missingRepresentationBucket struct {
	storage.Bucket
	writes atomic.Int32
}

func (b *missingRepresentationBucket) ReadChunkFile(context.Context, *object.ID, uint32) (storage.File, string, error) {
	return nil, "", os.ErrNotExist
}
func (b *missingRepresentationBucket) WriteChunkFile(ctx context.Context, id *object.ID, index uint32) (io.WriteCloser, string, error) {
	b.writes.Add(1)
	return b.Bucket.WriteChunkFile(ctx, id, index)
}

func TestRepresentationMissingChunkBypassesLegacyFill(t *testing.T) {
	_, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "origin bytes") })
	key, _ := object.NewResourceKey(target, "http-v2", "origin")
	repo, err := representation.NewIndexRepository(bucket.(storage.ControlStore), bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return bucket.Lookup(ctx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := repo.Update(t.Context(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := representation.NewHTTPSelector([]string{"Accept"}, http.Header{"Accept": {"image/jpeg"}})
	id, _ := key.RepresentationID(root.Resource.Incarnation, 0, ref.SelectorFingerprint, "generation")
	now := time.Now().Unix()
	ref.RepresentationID, ref.ResponseDate = id, now
	seedVaryTestObject(t, bucket, &object.Metadata{ID: id, RecordKind: object.RecordRepresentation, Code: 200, BlockSize: 64, Size: 5, RespUnix: now, ExpiresAt: now + 600,
		Headers: http.Header{"Vary": {"Accept"}, "Content-Length": {"5"}}, Representation: &object.RepresentationInfo{SchemaVersion: object.RepresentationSchemaVersion, ResourceID: key.ResourceID(), Incarnation: root.Resource.Incarnation,
			BodyGeneration: "generation", Kind: object.RepresentationOrigin, State: object.RepresentationComplete},
	}, "cache")
	if _, err := repo.Update(t.Context(), key, func(record *object.ResourceRecord) error { record.Variants = append(record.Variants, ref); return nil }); err != nil {
		t.Fatal(err)
	}
	broken := &missingRepresentationBucket{Bucket: bucket}
	rt := representationTestTransport(&representationTestStorage{bucket: broken}, &cachingOption{SliceSize: 64, IncludeQueryInCacheKey: true,
		representation: &representationLookup{repository: repo, namespace: "http-v2", isolationScope: "origin"}})
	resp, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
	if err != nil || body != "origin bytes" || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "BYPASS") {
		t.Fatalf("missing chunk: %v, %q, %v", resp, body, err)
	}
	if broken.writes.Load() != 0 {
		t.Fatal("legacy fill wrote into the selected generation")
	}
}
