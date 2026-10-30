package caching

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/proxy"
)

func TestWriteSessionKeepsOriginMetadataAndFixedTarget(t *testing.T) {
	body := strings.Repeat("a", 64) + strings.Repeat("b", 64) + strings.Repeat("c", 64)
	_, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		rng, err := xhttp.SingleRange(r.Header.Get("Range"), uint64(len(body)))
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Range", rng.ContentRange(uint64(len(body))))
		w.Header().Set("Content-Length", fmt.Sprint(rng.Length()))
		w.Header().Set("Cache-Control", "max-age=600")
		w.WriteHeader(206)
		fmt.Fprint(w, body[rng.Start:rng.End+1])
	})
	req := varyRequest(t, target, http.Header{"Range": {"bytes=65-70"}})
	id, _ := newObjectIDFromRequest(req, "", true)
	pc := NewProcessorChain(NewStateProcessor(), NewVaryProcessor(), NewRevalidateProcessor(), NewFileChangedProcessor(),
		NewFillRangeProcessor(WithFillRangePercent(100), WithChunkSize(64)))
	c := &Caching{req: req, ctx: req.Context(), id: id, bucket: bucket, origin: proxy.Transport{Proxy: proxy.GetProxy()}, processor: pc,
		log: log.Context(req.Context()), opt: &cachingOption{SliceSize: 64, AsyncFlushChunk: true}}
	resp, err := c.doProxy(req, false)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating frontend state before its Body is consumed must not redirect
	// the asynchronous worker or its failure cleanup.
	otherID := object.NewID(target + "?other-generation")
	c.id = otherID
	c.md = &object.Metadata{ID: otherID, Headers: http.Header{"Content-Length": {"999"}}, Size: 999}
	data, err := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if err != nil || closeErr != nil || string(data) != body[65:71] || resp.ContentLength != 6 || resp.Header.Get("Content-Range") != "bytes 65-70/192" {
		t.Fatal(resp.Header, string(data), err, closeErr)
	}
	stored, err := bucket.Lookup(t.Context(), id)
	if err != nil || stored.Size != 192 || stored.Headers.Get("Content-Length") != "192" || stored.Headers.Get("Content-Range") != "" || !stored.Chunks.Contains(1) {
		t.Fatal("client trimming contaminated stored headers or write target", stored, err)
	}
	if _, err := bucket.Lookup(t.Context(), otherID); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("background callback followed the changed ID", err)
	}
}

func TestRepresentationBackgroundRefreshOwnsSnapshotAndContext(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			originImage(w, "old body", "Accept", `"old"`)
			return
		}
		if r.Header.Get("If-None-Match") != `"old"` || r.Header.Get("Range") != "" {
			t.Error("background refresh lost its independent full validation request", r.Header)
		}
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		originImage(w, "new body", "Accept", `"new"`)
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	f.get(t, header)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := varyRequest(t, f.target, header).WithContext(ctx)
	pc := NewProcessorChain(NewStateProcessor(), NewVaryProcessor(), NewRevalidateProcessor())
	c, err := pc.preCacheProcessor(proxy.Transport{Proxy: proxy.GetProxy()}, &representationTestStorage{bucket: f.bucket}, f.opt, req)
	if err != nil || !c.hit {
		t.Fatal(err)
	}
	oldID := c.md.ID.Clone()
	background := c.cloneForBackground(req)
	done := make(chan struct{})
	go func() { (&RevalidateProcessor{}).asyncRevalidate(background, background.req); close(done) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("background request did not start")
	}
	// The original request can end while a cloned background lifecycle is
	// active. Frontend response assembly and mutations remain independent.
	cancel()
	c.md.Headers.Set("X-Frontend-Only", "value")
	c.variantRef.VaryFields[0] = "X-Frontend-Only"
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("background refresh did not finish")
	}
	if c.md.Headers.Get("ETag") != `"old"` || c.md.ID.Hash() != oldID.Hash() {
		t.Fatal("refresh changed frontend metadata")
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || selected.Representation.Headers.Get("ETag") != `"new"` || selected.Representation.Headers.Get("X-Frontend-Only") != "" || selected.Representation.ID.Hash() == oldID.Hash() {
		t.Fatal("background fill used frontend state or cancellation", selected, err)
	}
	if _, data := f.get(t, header); data != "new body" {
		t.Fatal(data)
	}
}
