package caching

import (
	"bytes"
	"image"
	"io"
	"net/http"
	"testing"

	"github.com/omalloc/tavern/server/middleware"
)

func TestManagedStorageFaultsFailClosedAndRetry(t *testing.T) {
	for _, fault := range []string{"disk-full", "home-unavailable", "control-write"} {
		t.Run(fault, func(t *testing.T) {
			f := newManagedFixture(t)
			f.provider.opt.VaryLimit = 1
			req, plan := f.request(t, "format=png", "image/png")
			source, err := f.provider.source(req, plan)
			if err != nil {
				t.Fatal(err)
			}
			source.close()
			bucket := f.store.bucket.(*sourceReadBucket)
			switch fault {
			case "disk-full":
				bucket.failData.Store(true)
			case "home-unavailable":
				bucket.failControlRead.Store(true)
			case "control-write":
				bucket.failControlWrite.Store(true)
			}
			response, err := f.provider.roundTrip(req, plan)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 502 || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Content-Type") == "image/jpeg" || bytes.Equal(body, f.raw) {
				t.Fatal("storage failure relaxed target format", response.StatusCode, response.Header)
			}
			switch fault {
			case "disk-full":
				if bucket.dataFailures.Load() == 0 {
					t.Fatal("ENOSPC was not exercised")
				}
			case "home-unavailable":
				if bucket.controlReadFailures.Load() == 0 {
					t.Fatal("missing home was not exercised")
				}
			case "control-write":
				if bucket.controlWriteFailures.Load() == 0 {
					t.Fatal("failed paired root write was not exercised")
				}
			}
			assertNoImageTemporaryFiles(t, f)
			bucket.failData.Store(false)
			bucket.failControlRead.Store(false)
			bucket.failControlWrite.Store(false)
			retry, data := managedHTTP(t, f, "GET", nil)
			decoded, format, err := image.Decode(bytes.NewReader(data))
			if err != nil || retry.StatusCode != 200 || format != "png" || decoded.Bounds().Dx() != 8 {
				t.Fatal("failed reservation or bytes prevented retry", retry.StatusCode, format, err)
			}
			if _, err = f.store.manager.CollectUnreachable(t.Context(), 128); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestTransparentHomeFailurePreservesOriginResponse(t *testing.T) {
	f := newManagedFixture(t)
	bucket := f.store.bucket.(*sourceReadBucket)
	bucket.failControlRead.Store(true)
	wanted := []byte("independent origin bytes")
	origin := middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/jpeg"}, "Content-Length": {"24"}}, ContentLength: int64(len(wanted)), Body: io.NopCloser(bytes.NewReader(wanted)), Request: req}, nil
	})
	opt := &cachingOption{SliceSize: 64, IncludeQueryInCacheKey: true, VaryLimit: 10, representation: f.provider.opt.representation}
	rt := bindCaching(origin, f.store, opt, NewProcessorChain(NewStateProcessor(), NewVaryProcessor(), NewRevalidateProcessor()).fill())
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/plain", nil)
	response, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(body, wanted) || response.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatal("transparent cache fault changed origin response", err)
	}
	if bucket.controlReadFailures.Load() == 0 {
		t.Fatal("home failure was not exercised")
	}
}
