package caching

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"github.com/andybalholm/brotli"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/lifecycle"
	"github.com/omalloc/tavern/server/middleware"
	planning "github.com/omalloc/tavern/transform"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func managedHTTP(t *testing.T, f *managedFixture, method string, headers http.Header) (*http.Response, []byte) {
	t.Helper()
	req, plan := f.request(t, "format=png", "image/png")
	req.Method = method
	for name, values := range headers {
		req.Header[name] = values
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
	return response, body
}
func TestManagedHTTPConditionsAndRangesUseFinalBytes(t *testing.T) {
	f := newManagedFixture(t)
	head, body := managedHTTP(t, f, "HEAD", nil)
	if head.StatusCode != 200 || len(body) != 0 || head.Header.Get("Content-Type") != "image/png" || head.ContentLength <= 0 {
		t.Fatalf("cold HEAD: %+v %q", head, body)
	}
	if f.origins.Load() != 1 || f.backend.calls.Load() != 1 {
		t.Fatal("cold HEAD not bound to complete conversion")
	}
	full, original := managedHTTP(t, f, "GET", nil)
	if full.ContentLength != int64(len(original)) || head.Header.Get("ETag") != full.Header.Get("ETag") || head.ContentLength != full.ContentLength {
		t.Fatal("HEAD selected a different representation")
	}
	etag := full.Header.Get("ETag")
	if etag == f.etag || !strings.HasPrefix(etag, `"sha256-`) {
		t.Fatal("source validator reused")
	}
	tests := []struct {
		name    string
		headers http.Header
		status  int
		want    []byte
	}{
		{"etag", http.Header{"If-None-Match": {etag}}, 304, nil},
		{"weak-etag", http.Header{"If-None-Match": {"W/" + etag}}, 304, nil},
		{"source-etag", http.Header{"If-None-Match": {f.etag}}, 200, original},
		{"opaque-case", http.Header{"If-None-Match": {strings.ToUpper(etag)}}, 200, original},
		{"if-match", http.Header{"If-Match": {f.etag}}, 412, nil},
		{"single", http.Header{"Range": {"bytes=1-7"}}, 206, original[1:8]},
		{"suffix", http.Header{"Range": {"bytes=-5"}}, 206, original[len(original)-5:]},
		{"if-range-match", http.Header{"Range": {"bytes=1-7"}, "If-Range": {etag}}, 206, original[1:8]},
		{"if-range-source", http.Header{"Range": {"bytes=1-7"}, "If-Range": {f.etag}}, 200, original},
		{"if-range-weak", http.Header{"Range": {"bytes=1-7"}, "If-Range": {"W/" + etag}}, 200, original},
		{"unsatisfiable", http.Header{"Range": {"bytes=99999-"}}, 416, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, data := managedHTTP(t, f, "GET", tt.headers)
			if response.StatusCode != tt.status {
				t.Fatalf("status=%d want=%d", response.StatusCode, tt.status)
			}
			if tt.status < 400 && !bytes.Equal(data, tt.want) {
				t.Fatalf("wrong final bytes: got %q want %q", data, tt.want)
			}
			if tt.status == 304 && (response.Body != http.NoBody || response.Header.Get("ETag") != etag) {
				t.Fatal("304 body or validator")
			}
			if tt.status == 206 && (response.ContentLength != int64(len(tt.want)) || response.Header.Get("Content-Range") == "") {
				t.Fatal("range length/headers")
			}
			if tt.status == 416 && response.Header.Get("Content-Range") != "bytes */"+strconv.Itoa(len(original)) {
				t.Fatal("416 does not describe final bytes")
			}
			vary := strings.ToLower(strings.Join(response.Header.Values("Vary"), ","))
			for _, field := range []string{"accept", "cache-control", "x-tenant"} {
				if !strings.Contains(vary, field) {
					t.Fatalf("Vary missing %s: %s", field, vary)
				}
			}
		})
	}
	reads := f.sourceReads.Load()
	managedHTTP(t, f, "HEAD", http.Header{"Range": {"bytes=1-7"}})
	managedHTTP(t, f, "GET", http.Header{"If-None-Match": {etag}})
	if f.origins.Load() != 1 || f.backend.calls.Load() != 1 || f.sourceReads.Load() != reads {
		t.Fatal("final conditional/HEAD caused source read or conversion")
	}
	multi, data := managedHTTP(t, f, "GET", http.Header{"Range": {"bytes=1-3,8-10"}})
	media, params, err := mime.ParseMediaType(multi.Header.Get("Content-Type"))
	if err != nil || media != "multipart/byteranges" || multi.StatusCode != 206 {
		t.Fatal(multi, err)
	}
	reader := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	for _, want := range [][]byte{original[1:4], original[8:11]} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		actual, err := io.ReadAll(part)
		if err != nil || !bytes.Equal(actual, want) {
			t.Fatal("multipart contains source/different product bytes", err)
		}
	}
	if _, err = reader.NextPart(); err != io.EOF {
		t.Fatal(err)
	}
	if multi.ContentLength != int64(len(data)) {
		t.Fatal("multipart ContentLength mismatch")
	}
}
func TestManagedHTTPColdRangeSameProduct(t *testing.T) {
	f := newManagedFixture(t)
	response, partial := managedHTTP(t, f, "GET", http.Header{"Range": {"bytes=2-6"}})
	full, original := managedHTTP(t, f, "GET", nil)
	if response.StatusCode != 206 || !bytes.Equal(partial, original[2:7]) || response.Header.Get("ETag") != full.Header.Get("ETag") {
		t.Fatal("cold range not taken from final product")
	}
	if f.origins.Load() != 1 || f.backend.calls.Load() != 1 {
		t.Fatal("range and GET did not reuse conversion")
	}
}

func managedPlannedHTTP(t *testing.T, f *managedFixture, query, accept string, headers http.Header, fallback bool) (*http.Response, []byte) {
	t.Helper()
	req, plan := f.request(t, query, accept)
	plan.FallbackToSource = fallback
	for name, values := range headers {
		req.Header[name] = values
	}
	response, err := f.provider.roundTrip(req, plan)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}
func TestManagedHTTPNoTransformAndFallbackIsolation(t *testing.T) {
	for _, private := range []bool{false, true} {
		for _, encoding := range []string{"", "gzip", "br"} {
			t.Run(fmt.Sprintf("original/private=%v/%s", private, encoding), func(t *testing.T) {
				f := newManagedFixture(t)
				f.encoding = encoding
				if private {
					f.cacheControl = "private, max-age=600"
				}
				response, data := managedPlannedHTTP(t, f, "", "image/png, image/jpeg;q=0.5", http.Header{"Cache-Control": {"no-transform"}}, false)
				if response.StatusCode != 200 || response.Header.Get("Content-Type") != "image/jpeg" || response.Header.Get("ETag") != f.etag || response.Header.Get("Content-Encoding") != encoding || response.ContentLength != int64(len(data)) {
					t.Fatalf("not original representation: %+v", response)
				}
				decoded := data
				if encoding == "gzip" {
					reader, err := gzip.NewReader(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					decoded, err = io.ReadAll(reader)
					reader.Close()
					if err != nil {
						t.Fatal(err)
					}
				} else if encoding == "br" {
					var err error
					decoded, err = io.ReadAll(brotli.NewReader(bytes.NewReader(data)))
					if err != nil {
						t.Fatal(err)
					}
				}
				if !bytes.Equal(decoded, f.raw) || f.backend.calls.Load() != 0 {
					t.Fatal("no-transform changed source pixels/bytes or invoked encoder")
				}
				conflict, _ := managedPlannedHTTP(t, f, "format=png", "image/png, image/jpeg", http.Header{"Cache-Control": {"no-transform"}}, true)
				if conflict.StatusCode != 406 {
					t.Fatal("explicit incompatible no-transform did not fail")
				}
				normal, data := managedPlannedHTTP(t, f, "format=png", "image/png", nil, false)
				if normal.StatusCode != 200 || normal.Header.Get("Content-Type") != "image/png" || bytes.Equal(data, f.raw) || f.backend.calls.Load() != 1 {
					t.Fatal("original response polluted target cache")
				}
			})
		}
	}
	t.Run("origin-no-transform", func(t *testing.T) {
		f := newManagedFixture(t)
		f.cacheControl = "max-age=600, no-transform"
		response, _ := managedPlannedHTTP(t, f, "", "image/png, image/jpeg;q=0.5", nil, false)
		if response.StatusCode != 200 || response.Header.Get("Content-Type") != "image/jpeg" || f.backend.calls.Load() != 0 {
			t.Fatal("origin no-transform ignored")
		}
		conflict, _ := managedPlannedHTTP(t, f, "format=png", "image/png", nil, false)
		if conflict.StatusCode != 406 {
			t.Fatal("origin no-transform target conflict ignored")
		}
	})
	t.Run("fallback", func(t *testing.T) {
		f := newManagedFixture(t)
		f.backend.failure.Store(true)
		rejected, _ := managedPlannedHTTP(t, f, "format=png", "image/png, image/jpeg", nil, false)
		if rejected.StatusCode != 502 || rejected.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("default conversion error did not fail closed")
		}
		original, data := managedPlannedHTTP(t, f, "format=png", "image/png, image/jpeg", nil, true)
		if original.StatusCode != 200 || original.Header.Get("Content-Type") != "image/jpeg" || original.Header.Get("ETag") != f.etag || !bytes.Equal(data, f.raw) || original.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("fallback not original/no-store")
		}
		unacceptable, _ := managedPlannedHTTP(t, f, "format=png", "image/png, image/jpeg;q=0", nil, true)
		if unacceptable.StatusCode != 502 {
			t.Fatal("fallback violated explicit source exclusion")
		}
		f.backend.failure.Store(false)
		target, _ := managedPlannedHTTP(t, f, "format=png", "image/png", nil, true)
		if target.StatusCode != 200 || target.Header.Get("Content-Type") != "image/png" || f.origins.Load() != 1 {
			t.Fatal("fallback contaminated target or evicted source")
		}
	})
	t.Run("encoding-not-acceptable", func(t *testing.T) {
		f := newManagedFixture(t)
		response, _ := managedPlannedHTTP(t, f, "format=png", "image/png", http.Header{"Accept-Encoding": {"identity;q=0, gzip"}}, false)
		if response.StatusCode != 406 {
			t.Fatal("identity output violated coding exclusion")
		}
	})
}

type blockedManagedClose struct {
	io.Reader
	entered, release chan struct{}
}

func (r *blockedManagedClose) Close() error { close(r.entered); <-r.release; return nil }
func TestManagedResponseCloseWaitsForReaderAndPinBeforeDrain(t *testing.T) {
	var work lifecycle.Group
	ctx, finish, err := work.Begin(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://example.com/images/a", nil)
	reader := &blockedManagedClose{Reader: strings.NewReader("complete final bytes"), entered: make(chan struct{}), release: make(chan struct{})}
	pinClosed := make(chan struct{})
	result := &managedResult{Metadata: &object.Metadata{Size: uint64(len("complete final bytes")), Headers: http.Header{"Content-Type": {"image/png"}}}, open: func(context.Context) (io.ReadCloser, error) { return reader, nil }, finish: func() error { close(pinClosed); return nil }}
	response, err := respondManagedResult(req, result)
	if err != nil {
		finish()
		t.Fatal(err)
	}
	finishCacheTask(ctx, response, finish)
	var first [1]byte
	if _, err = io.ReadFull(response.Body, first[:]); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { response.Body.Close(); close(closed) }()
	<-reader.entered
	drained := make(chan error, 1)
	go func() { drained <- work.Drain(context.Background()) }()
	select {
	case <-closed:
		t.Fatal("client close returned before local reader finished")
	default:
	}
	select {
	case <-drained:
		t.Fatal("Drain returned before reader/pin release")
	default:
	}
	close(reader.release)
	<-closed
	if err = <-drained; err != nil {
		t.Fatal(err)
	}
	select {
	case <-pinClosed:
	default:
		t.Fatal("producer pin not closed")
	}
}

func TestManagedMetadataPreservationFailsClosedAndCanKeepOriginal(t *testing.T) {
	f := newManagedFixture(t)
	segment := []byte{0xff, 0xe1, 0, 10, 'E', 'x', 'i', 'f', 0, 0, 'I', 'I'}
	f.raw = append(append(append([]byte{}, f.raw[:2]...), segment...), f.raw[2:]...)
	f.provider.inspector = planning.ImageInspector{}
	rejected, _ := managedPlannedHTTP(t, f, "format=png", "image/png, image/jpeg", nil, false)
	if rejected.StatusCode != 415 || f.backend.calls.Load() != 0 {
		t.Fatal("unverified EXIF preservation was silently converted")
	}
	fallback, data := managedPlannedHTTP(t, f, "format=png", "image/png, image/jpeg", nil, true)
	if fallback.StatusCode != 200 || !bytes.Equal(data, f.raw) || fallback.Header.Get("ETag") != f.etag || fallback.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("EXIF source fallback did not preserve original bytes")
	}
	original, data := managedPlannedHTTP(t, f, "format=source", "image/jpeg", nil, false)
	if original.StatusCode != 200 || !bytes.Equal(data, f.raw) || f.backend.calls.Load() != 0 {
		t.Fatal("preserve-source unnecessarily rejected metadata")
	}
}

func TestNoTransformUsesOpaqueOriginalPathWithoutDecoder(t *testing.T) {
	f := newManagedFixture(t)
	f.provider.limits.Pixels = 1 // Raw delivery must not consume a decode budget.
	f.cacheControl = "private, max-age=600"
	response, data := managedPlannedHTTP(t, f, "", "image/jpeg", http.Header{"Cache-Control": {"no-transform"}}, false)
	if response.StatusCode != 200 || !bytes.Equal(data, f.raw) || f.inspector.calls.Load() != 0 || f.backend.calls.Load() != 0 {
		t.Fatal("no-transform passed through image decoding")
	}
	raw := []byte("an opaque original representation")
	f.provider.origin = middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/octet-stream"}, "Content-Length": {strconv.Itoa(len(raw))}, "Cache-Control": {"no-transform, private"}}, ContentLength: int64(len(raw)), Body: io.NopCloser(bytes.NewReader(raw)), Request: req}, nil
	})
	response, data = managedPlannedHTTP(t, f, "", "*/*", nil, false)
	if response.StatusCode != 200 || !bytes.Equal(data, raw) || response.Header.Get("Content-Type") != "application/octet-stream" || f.inspector.calls.Load() != 0 {
		t.Fatal("response no-transform did not preserve opaque source")
	}
}

func TestOriginalSourceAcceptChecksActualMediaParameters(t *testing.T) {
	f := newManagedFixture(t)
	origin := f.provider.origin
	f.provider.origin = middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		response, err := origin.RoundTrip(req)
		if response != nil {
			response.Header.Set("Content-Type", "image/jpeg;profile=foo")
		}
		return response, err
	})
	accepted, data := managedPlannedHTTP(t, f, "format=source", "image/jpeg;profile=foo", nil, false)
	if accepted.StatusCode != 200 || !bytes.Equal(data, f.raw) {
		t.Fatal("explicit original source lost media parameters")
	}
	excluded, _ := managedPlannedHTTP(t, f, "format=source", "image/jpeg;profile=foo;q=0, image/jpeg", nil, false)
	if excluded.StatusCode != 406 {
		t.Fatal("raw source ignored a specific parameter exclusion")
	}
	excluded, _ = managedPlannedHTTP(t, f, "", "image/jpeg;profile=foo;q=0, image/jpeg", nil, false)
	if excluded.StatusCode != 406 {
		t.Fatal("final actual MIME was not checked")
	}
}
