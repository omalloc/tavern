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
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

func TestRepresentationUnknownLengthPublishesOnlyAtEOF(t *testing.T) {
	var calls atomic.Int32
	want := strings.Repeat("unknown-size-", 20)
	f := newOriginFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Vary", "Accept")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, want)
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	resp, err := f.rt.RoundTrip(varyRequest(t, f.target, header))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.ContentLength != -1 || resp.Header.Get("Content-Length") != "" {
		t.Fatal("origin unknown length changed before consumption", resp.Header)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	if len(f.root(t).Resource.Variants) != 0 {
		t.Fatal("unknown total published before EOF")
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || !selected.Representation.HasComplete() || selected.Representation.Size != uint64(len(want)) ||
		selected.Representation.Headers.Get("Content-Length") != strconv.Itoa(len(want)) || resp.Header.Get("Content-Length") != "" {
		t.Fatal("EOF did not separate stored size from original response", selected, err)
	}
	hit, body := f.get(t, header)
	if body != want || hit.ContentLength != int64(len(want)) || calls.Load() != 1 {
		t.Fatal(hit.Header, body, calls.Load())
	}
}

func TestRepresentationPartialGenerationAndHeaderIsolation(t *testing.T) {
	var calls atomic.Int32
	body := strings.Repeat("A", 64) + strings.Repeat("B", 64) + strings.Repeat("C", 22)
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Vary", "Accept")
		// Deliberately no validator: all bytes in one partial generation come
		// from one origin response, and missing blocks must get a new ID.
		if raw := r.Header.Get("Range"); raw != "" {
			rng, err := xhttp.SingleRange(raw, uint64(len(body)))
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Range", rng.ContentRange(uint64(len(body))))
			w.Header().Set("Content-Length", fmt.Sprint(rng.Length()))
			w.WriteHeader(206)
			fmt.Fprint(w, body[rng.Start:rng.End+1])
			return
		}
		originImage(w, body, "Accept", "")
	})
	header := http.Header{"Accept": {"image/jpeg"}, "Range": {"bytes=3-127"}}
	resp, data := f.get(t, header)
	if resp.StatusCode != 206 || resp.ContentLength != 125 || data != body[3:128] || resp.Header.Get("Content-Range") != "bytes 3-127/150" {
		t.Fatal("changed original range response", resp.Header, data)
	}
	first, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil {
		t.Fatal(err)
	}
	md := first.Representation
	if md.Size != 150 || md.Headers.Get("Content-Length") != "150" || md.Headers.Get("Content-Range") != "" || md.Code != 200 ||
		md.HasComplete() || md.Representation.State != object.RepresentationFilling || md.Chunks.Count() != 1 || !md.Chunks.Contains(1) {
		t.Fatal("partial descriptor confused response length or boundary chunks", md)
	}
	// A strict subrange entirely inside the confirmed chunk can hit.
	header.Set("Range", "bytes=70-79")
	resp, data = f.get(t, header)
	if data != body[70:80] || resp.ContentLength != 10 || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "HIT") || calls.Load() != 1 {
		t.Fatal("confirmed chunk was not reusable", resp.Header, data)
	}
	// A hole triggers a full rebuild, even at the same Date. Client cropping
	// cannot change the complete descriptor or stop the writer before EOF.
	header.Set("Range", "bytes=128-149")
	resp, data = f.get(t, header)
	if resp.StatusCode != 206 || resp.ContentLength != 22 || data != body[128:] || resp.Header.Get("Content-Range") != "bytes 128-149/150" {
		t.Fatal("rebuilt response lost the client's range", resp.Header, data)
	}
	second, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || second.Representation.ID.Hash() == md.ID.Hash() || !second.Representation.HasComplete() || second.Representation.Headers.Get("Content-Length") != "150" {
		t.Fatal("range response reused an unproven generation", second, err)
	}
	// A full GET can now reuse the complete rebuilt generation.
	header.Del("Range")
	f.get(t, header)
	complete, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || !complete.Representation.HasComplete() || complete.Representation.ID.Hash() != second.Representation.ID.Hash() {
		t.Fatal("full request did not reuse the rebuilt generation", complete, err)
	}
	f.get(t, header)
	if calls.Load() != 2 {
		t.Fatal("rebuilt representation did not hit", calls.Load())
	}
	// Logical replacement preserves old bytes until P2 reader leases/GC.
	file, _, err := f.bucket.ReadChunkFile(t.Context(), md.ID, 1)
	if err != nil {
		t.Fatal("replacement deleted a reader's old generation", err)
	}
	file.Close()
}

func TestRepresentationFillCancellationClosesOriginAndReleasesBudget(t *testing.T) {
	originClosed := make(chan struct{})
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "a" {
			originImage(w, "other", "Accept", "")
			return
		}
		w.Header().Set("Vary", "Accept")
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Content-Length", "128")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, strings.Repeat("a", 64))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(originClosed)
	})
	f.opt.VaryLimit = 1
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := varyRequest(t, f.target, http.Header{"Accept": {"a"}}).WithContext(ctx)
	resp, err := f.rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if _, err := io.ReadFull(resp.Body, make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-originClosed:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not close upstream")
	}
	// Close also joins any concurrent cancellation cleanup.
	resp.Body.Close()
	if len(f.root(t).Resource.Variants) != 0 {
		t.Fatal("cancelled fill published its confirmed prefix")
	}
	f.get(t, http.Header{"Accept": {"b"}})
	if len(f.root(t).Resource.Variants) != 1 {
		t.Fatal("cancelled fill leaked its reservation")
	}
}

func TestRepresentationFailedNewGenerationPreservesPublishedBody(t *testing.T) {
	var phase atomic.Int32
	f := newOriginFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		originImage(w, strings.Repeat(fmt.Sprint(phase.Load()), 128), "Accept", `"version"`)
	})
	header := http.Header{"Accept": {"image/jpeg"}}
	f.get(t, header)
	old, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil {
		t.Fatal(err)
	}
	phase.Store(1)
	f.clock.Add(601)
	broken := &failingRepresentationBucket{Bucket: f.bucket}
	broken.fail.Store(true)
	f.rt = representationTestTransport(&representationTestStorage{bucket: broken}, f.opt)
	_, data := f.get(t, header)
	if data != strings.Repeat("1", 128) {
		t.Fatal("write failure damaged origin passthrough", data)
	}
	failedID := broken.lastID.Load()
	if failedID == nil || failedID.Hash() == old.Representation.ID.Hash() {
		t.Fatal("failed write targeted published generation")
	}
	if file, _, err := f.bucket.ReadChunkFile(t.Context(), failedID, 0); !errors.Is(err, os.ErrNotExist) {
		if file != nil {
			file.Close()
		}
		t.Fatal("failed generation left unconfirmed bytes", err)
	}
	current, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || current.Representation.ID.Hash() != old.Representation.ID.Hash() {
		t.Fatal("failure retired the published generation", current, err)
	}
	file, _, err := f.bucket.ReadChunkFile(t.Context(), old.Representation.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	bytes, err := io.ReadAll(file)
	if err != nil || string(bytes) != strings.Repeat("0", 64) {
		t.Fatal("new response overwrote old bytes", string(bytes), err)
	}
}

func TestRepresentationInterruptedBodiesNeverPublishPrefixes(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint("unknown=", unknown), func(t *testing.T) {
			var calls atomic.Int32
			f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Accept") == "b" {
					originImage(w, "other selection", "Accept", "")
					return
				}
				w.Header().Set("Vary", "Accept")
				w.Header().Set("Cache-Control", "max-age=600")
				if !unknown {
					w.Header().Set("Content-Length", "128")
				}
				w.(http.Flusher).Flush()
				length := 64
				if unknown {
					// Leave unread bytes, so an early Close cannot legitimately
					// coincide with the origin's normal EOF.
					length = 128
				}
				fmt.Fprint(w, strings.Repeat("a", length))
			})
			f.opt.VaryLimit = 1
			resp, err := f.rt.RoundTrip(varyRequest(t, f.target, http.Header{"Accept": {"a"}}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(resp.Body, make([]byte, 64)); err != nil {
				resp.Body.Close()
				t.Fatal(err)
			}
			if !unknown {
				if _, err := io.ReadAll(resp.Body); !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Error("truncated response lost its read error", err)
				}
			}
			resp.Body.Close()
			if len(f.root(t).Resource.Variants) != 0 {
				t.Fatal("interrupted body published its prefix")
			}
			requireEmptyVaryCache(t, f.bucket)
			f.get(t, http.Header{"Accept": {"b"}})
			if calls.Load() != 2 || len(f.root(t).Resource.Variants) != 1 {
				t.Fatal("interrupted body leaked budget", calls.Load())
			}
		})
	}
}

func TestRepresentationFull206BecomesComplete(t *testing.T) {
	var calls atomic.Int32
	body := strings.Repeat("origin", 25)
	f := newOriginFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Content-Range", "bytes 0-149/150")
		w.WriteHeader(206)
		fmt.Fprint(w, body)
	})
	resp, data := f.get(t, http.Header{"Range": {"bytes=0-149"}})
	if resp.StatusCode != 206 || data != body {
		t.Fatal(resp.Header, data)
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, nil)
	if err != nil || !selected.Representation.HasComplete() {
		t.Fatal("full 206 did not complete its generation", selected, err)
	}
	resp, data = f.get(t, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Range") != "" || data != body || calls.Load() != 1 {
		t.Fatal("full GET could not reuse the complete 206 bytes", resp.Header, data, calls.Load())
	}
}
