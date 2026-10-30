package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	middlewarev1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/server/middleware"
	"github.com/omalloc/tavern/storage"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
	planning "github.com/omalloc/tavern/transform"
)

func publicImageCapabilities() transformv1.BackendCapabilities {
	caps := transformv1.BackendCapabilities{SchemaVersion: 1, Inputs: []string{"image/jpeg", "image/png"}, Outputs: []transformv1.Codec{{MIME: "image/jpeg", QualityMin: 1, QualityMax: 100}, {MIME: "image/png", Lossless: true, CompressionMax: 9}}, PolicyVersion: "test-policy-v1", EncoderVersion: "test-encoder-v1"}
	for _, input := range caps.Inputs {
		for _, output := range caps.Outputs {
			caps.Conversions = append(caps.Conversions, transformv1.Conversion{InputMIME: input, OutputMIME: output.MIME})
		}
	}
	return caps
}
func publicTransformConfig(endpoint, directory string) *middlewarev1.Middleware {
	return &middlewarev1.Middleware{Name: "transform", Options: map[string]any{"enabled": true, "routes": []map[string]any{{"path_prefix": "/images/", "mode": "auto", "formats": []string{"image/png", "image/jpeg"}, "quality": 82, "policy_version": "test-route-v1", "animation_policy": "preserve_source"}}, "backend": map[string]any{"name": "http", "endpoint": endpoint, "capabilities": publicImageCapabilities()}, "limits": map[string]any{"max_input_bytes": 1 << 20, "max_output_bytes": 1 << 20, "max_pixels": 1024, "concurrency": 2, "queue_size": 32, "timeout": "2s", "temporary_directory": directory}, "startup_timeout": "10s"}}
}
func publicImageWorker(t *testing.T, calls *atomic.Int32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get(planning.SpecHeader))
		if err != nil {
			t.Error(err)
			http.Error(w, "bad spec", 400)
			return
		}
		var spec transformv1.TransformSpec
		if err = json.Unmarshal(raw, &spec); err != nil {
			t.Error(err)
			http.Error(w, "bad spec", 400)
			return
		}
		img, _, err := image.Decode(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "bad input", 400)
			return
		}
		var output bytes.Buffer
		if spec.OutputMIME == "image/png" {
			err = png.Encode(&output, img)
		} else {
			err = jpeg.Encode(&output, img, &jpeg.Options{Quality: *spec.Quality})
		}
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", spec.OutputMIME)
		w.Header().Set(planning.EncoderHeader, "test-encoder-v1")
		w.Write(output.Bytes())
	}
}
func TestPublicManagedPipelineConfigurationAndReuse(t *testing.T) {
	old := storage.Current()
	defer storage.SetDefault(old)
	path, state := t.TempDir(), t.TempDir()
	storageConfig := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", SliceSize: 64, DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: state}, Buckets: []*conf.Bucket{{Path: path, MaxObjectLimit: 100}}}
	store, err := storage.New(storageConfig, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storage.SetDefault(store)
	var rpc, origins atomic.Int32
	worker := httptest.NewServer(publicImageWorker(t, &rpc))
	defer worker.Close()
	temp := t.TempDir()
	transformConfig := publicTransformConfig(worker.URL+"/v1/transform", temp)
	cacheConfig := &middlewarev1.Middleware{Name: "caching", Options: map[string]any{"include_query_in_cache_key": true, "representation": map[string]any{"enabled": true, "namespace": "public-image-test"}}}
	config := &conf.Bootstrap{Storage: storageConfig, Server: &conf.Server{Middleware: []*middlewarev1.Middleware{transformConfig, {Name: "multirange"}, cacheConfig}}}
	imageBytes := func() []byte {
		canvas := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for y := 0; y < 16; y++ {
			for x := 0; x < 16; x++ {
				canvas.SetRGBA(x, y, color.RGBA{uint8(x * 16), uint8(y * 16), uint8((x + y) * 8), 255})
			}
		}
		var output bytes.Buffer
		if err := jpeg.Encode(&output, canvas, &jpeg.Options{Quality: 95}); err != nil {
			t.Fatal(err)
		}
		return output.Bytes()
	}()
	origin := middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		origins.Add(1)
		if r.Method != "GET" || r.Header.Get("Range") != "" || r.Header.Get("If-None-Match") != "" || strings.Contains(r.URL.RawQuery, "format=") || strings.Contains(r.URL.RawQuery, "quality=") {
			t.Errorf("target request leaked to source: %s %s %v", r.Method, r.URL, r.Header)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/jpeg"}, "Content-Length": {strconv.Itoa(len(imageBytes))}, "Cache-Control": {"max-age=600"}, "Etag": {`"Source-A"`}, "Vary": {"X-Tenant"}}, ContentLength: int64(len(imageBytes)), Body: io.NopCloser(bytes.NewReader(imageBytes)), Request: r}, nil
	})
	server := &HTTPServer{config: config}
	rt, err := server.buildMiddlewareChain(origin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, cleanup := range server.cleanups {
			if cleanup != nil {
				cleanup()
			}
		}
	}()
	probeCalls := rpc.Load()
	if probeCalls == 0 {
		t.Fatal("public startup skipped actual worker gate")
	}
	request := func(method, query string, headers http.Header) (*http.Response, []byte, error) {
		req, _ := http.NewRequestWithContext(t.Context(), method, "https://example.com/images/source.jpg?"+query, nil)
		req.Header.Set("Accept", "image/png, image/jpeg;q=0.5")
		for name, values := range headers {
			req.Header[name] = values
		}
		response, err := rt.RoundTrip(req)
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		return response, data, err
	}
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	bodies := make(chan []byte, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, data, err := request("GET", "format=png", nil)
			if err == nil && response.StatusCode != 200 {
				err = fmt.Errorf("status %d", response.StatusCode)
			}
			if err != nil {
				failures <- err
				return
			}
			bodies <- data
		}()
	}
	wg.Wait()
	close(failures)
	close(bodies)
	for err := range failures {
		t.Error(err)
	}
	var first []byte
	for data := range bodies {
		if first == nil {
			first = data
		} else if !bytes.Equal(first, data) {
			t.Fatal("same spec produced different public bytes")
		}
	}
	if origins.Load() != 1 || rpc.Load() != probeCalls+1 {
		t.Fatalf("cold work not collapsed: origin=%d transform=%d", origins.Load(), rpc.Load()-probeCalls)
	}
	decoded, format, err := image.Decode(bytes.NewReader(first))
	if err != nil || format != "png" || decoded.Bounds().Dx() != 16 {
		t.Fatal("not complete PNG", format, err)
	}
	full, _, err := request("GET", "format=png", nil)
	if err != nil {
		t.Fatal(err)
	}
	conditional, body, err := request("GET", "format=png", http.Header{"If-None-Match": {full.Header.Get("ETag")}})
	if err != nil || conditional.StatusCode != 304 || len(body) != 0 {
		t.Fatal("public conditional", err)
	}
	multi, body, err := request("GET", "format=png", http.Header{"Range": {"bytes=1-3,8-10"}})
	if err != nil || multi.StatusCode != 206 || !strings.HasPrefix(multi.Header.Get("Content-Type"), "multipart/byteranges;") {
		t.Fatal("managed multirange bypass", multi, err)
	}
	if int64(len(body)) != multi.ContentLength || origins.Load() != 1 || rpc.Load() != probeCalls+1 {
		t.Fatal("public multi-range changed conversion identity")
	}

	// A process/storage reopen must reach the same published product without
	// opening the source or converting again (startup probes are counted apart).
	store.Close()
	store, err = storage.New(storageConfig, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storage.SetDefault(store)
	warm := &HTTPServer{config: config}
	reopenProbeStart := rpc.Load()
	rt, err = warm.buildMiddlewareChain(origin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, cleanup := range warm.cleanups {
			if cleanup != nil {
				cleanup()
			}
		}
	}()
	probeCalls += rpc.Load() - reopenProbeStart
	reopened, reopenedBytes, err := request("GET", "format=png", nil)
	if err != nil || !bytes.Equal(reopenedBytes, first) || !strings.HasPrefix(reopened.Header.Get("X-Cache"), "HIT") || origins.Load() != 1 || rpc.Load() != probeCalls+1 {
		t.Fatalf("reopen failed product reuse: origin=%d conversions=%d error=%v", origins.Load(), rpc.Load()-probeCalls, err)
	}
	if err = store.PURGE("https://example.com/images/source.jpg?format=png", storagev1.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = request("GET", "format=png", nil); err != nil {
		t.Fatal(err)
	}
	if origins.Load() != 2 || rpc.Load() != probeCalls+2 {
		t.Fatal("public parameter-mapped PURGE missed source/product")
	}
	// Restart with conversion disabled; retained routes must still map PURGE even
	// with an unavailable worker and representation reads disabled.
	store.Close()
	store, err = storage.New(storageConfig, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storage.SetDefault(store)
	transformConfig.Options["enabled"] = false
	cacheConfig.Options["representation"] = map[string]any{"enabled": false, "namespace": "public-image-test"}
	rollback := &HTTPServer{config: config}
	rollbackRT, err := rollback.buildMiddlewareChain(origin)
	if err != nil {
		t.Fatalf("rollback contacted missing worker or required reads: %v", err)
	}
	beforePaused := origins.Load()
	for _, method := range []string{"GET", "HEAD"} {
		req, _ := http.NewRequestWithContext(t.Context(), method, "https://example.com/images/source.jpg?format=png", nil)
		req.Header.Set("Accept", "image/png")
		response, err := rollbackRT.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 503 || response.Header.Get("Cache-Control") != "no-store" || origins.Load() != beforePaused || (method == "HEAD" && len(body) != 0) {
			t.Fatal("paused mapped route escaped to raw origin", response, err)
		}
	}
	ordinary, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/ordinary.jpg", nil)
	response, err := rollbackRT.RoundTrip(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(data, imageBytes) || origins.Load() != beforePaused+1 {
		t.Fatal("rollback stopped ordinary transparent proxy", err)
	}
	originAfterRollback := origins.Load()
	for _, cleanup := range rollback.cleanups {
		if cleanup != nil {
			cleanup()
		}
	}
	if err = store.PURGE("https://example.com/images/source.jpg?format=png", storagev1.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	if rpc.Load() != probeCalls+2 {
		t.Fatal("disabled routes called worker")
	}
	// Reopen enabled: the rollback-time mapped barrier must force new bytes.
	store.Close()
	store, err = storage.New(storageConfig, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storage.SetDefault(store)
	transformConfig.Options["enabled"] = true
	cacheConfig.Options["representation"] = map[string]any{"enabled": true, "namespace": "public-image-test"}
	resumed := &HTTPServer{config: config}
	rt, err = resumed.buildMiddlewareChain(origin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, cleanup := range resumed.cleanups {
			if cleanup != nil {
				cleanup()
			}
		}
	}()
	afterProbe := rpc.Load()
	if _, _, err = request("GET", "format=png", nil); err != nil {
		t.Fatal(err)
	}
	if origins.Load() != originAfterRollback+1 || rpc.Load() != afterProbe+1 {
		t.Fatal("rollback PURGE allowed stale derived hit after reenable")
	}
}

func TestPublicManagedRealWebP(t *testing.T) {
	if os.Getenv("TAVERN_TEST_CWEBP") != "1" {
		t.Skip("explicit public WebP acceptance; set TAVERN_TEST_CWEBP=1")
	}
	cwebp, err := exec.LookPath("cwebp")
	if err != nil {
		t.Fatal(err)
	}
	var rpc, origins atomic.Int32
	standard := publicImageWorker(t, &rpc)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoded, _ := base64.RawURLEncoding.DecodeString(r.Header.Get(planning.SpecHeader))
		var spec transformv1.TransformSpec
		if err := json.Unmarshal(encoded, &spec); err != nil {
			http.Error(w, "bad spec", 400)
			return
		}
		if spec.OutputMIME != "image/webp" {
			standard(w, r)
			return
		}
		rpc.Add(1)
		img, _, err := image.Decode(r.Body)
		if err != nil {
			http.Error(w, "bad input", 400)
			return
		}
		dir, err := os.MkdirTemp("", "tavern-public-webp-*")
		if err != nil {
			t.Error(err)
			return
		}
		defer os.RemoveAll(dir)
		input, output := filepath.Join(dir, "in.png"), filepath.Join(dir, "out.webp")
		var pngBytes bytes.Buffer
		if err = png.Encode(&pngBytes, img); err == nil {
			err = os.WriteFile(input, pngBytes.Bytes(), 0600)
		}
		if err != nil {
			t.Error(err)
			return
		}
		args := []string{"-quiet", "-exact"}
		if spec.Lossless {
			args = append(args, "-lossless")
		} else {
			args = append(args, "-q", strconv.Itoa(*spec.Quality))
		}
		args = append(args, input, "-o", output)
		if log, err := exec.CommandContext(r.Context(), cwebp, args...).CombinedOutput(); err != nil {
			t.Errorf("cwebp: %v %s", err, log)
			http.Error(w, "encoder error", 500)
			return
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set(planning.EncoderHeader, "test-encoder-v1")
		w.Write(data)
	}))
	defer worker.Close()
	old := storage.Current()
	defer storage.SetDefault(old)
	storageConfig := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", SliceSize: 64, DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()}, Buckets: []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}
	store, err := storage.New(storageConfig, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storage.SetDefault(store)
	config := publicTransformConfig(worker.URL+"/v1/transform", t.TempDir())
	caps := publicImageCapabilities()
	caps.Inputs = append(caps.Inputs, "image/webp")
	caps.Outputs = append(caps.Outputs, transformv1.Codec{MIME: "image/webp", QualityMin: 1, QualityMax: 100, Lossless: true})
	caps.Conversions = nil
	for _, input := range caps.Inputs {
		for _, output := range caps.Outputs {
			caps.Conversions = append(caps.Conversions, transformv1.Conversion{InputMIME: input, OutputMIME: output.MIME})
		}
	}
	config.Options["backend"].(map[string]any)["capabilities"] = caps
	config.Options["routes"].([]map[string]any)[0]["formats"] = []string{"image/webp", "image/png", "image/jpeg"}
	canvas := image.NewRGBA(image.Rect(0, 0, 16, 16))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{80, 130, 180, 255}), image.Point{}, draw.Src)
	var jpegBytes, pngBytes bytes.Buffer
	jpeg.Encode(&jpegBytes, canvas, &jpeg.Options{Quality: 95})
	png.Encode(&pngBytes, canvas)
	var sourceMu sync.Mutex
	sourceMIME := "image/jpeg"
	sourceBytes := jpegBytes.Bytes()
	origin := middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		origins.Add(1)
		sourceMu.Lock()
		defer sourceMu.Unlock()
		data := append([]byte{}, sourceBytes...)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {sourceMIME}, "Content-Length": {strconv.Itoa(len(data))}, "Cache-Control": {"max-age=600"}}, ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
	})
	server := &HTTPServer{config: &conf.Bootstrap{Storage: storageConfig, Server: &conf.Server{Middleware: []*middlewarev1.Middleware{config, {Name: "multirange"}, {Name: "caching", Options: map[string]any{"include_query_in_cache_key": true, "representation": map[string]any{"enabled": true}}}}}}}
	rt, err := server.buildMiddlewareChain(origin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, cleanup := range server.cleanups {
			if cleanup != nil {
				cleanup()
			}
		}
	}()
	probeCalls := rpc.Load()
	target := "https://example.com/images/input"
	get := func(format string) error {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", target+"?format="+format, nil)
		req.Header.Set("Accept", "image/"+format)
		response, err := rt.RoundTrip(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		img, actual, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return err
		}
		if response.StatusCode != 200 || response.Header.Get("Content-Type") != "image/"+format || actual != format || img.Bounds() != canvas.Bounds() {
			return fmt.Errorf("incorrect public %s image: status=%d actual=%s", format, response.StatusCode, actual)
		}
		return nil
	}
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		format := "webp"
		if i%2 == 1 {
			format = "png"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := get(format); err != nil {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if origins.Load() != 1 || rpc.Load() != probeCalls+2 {
		t.Fatalf("mixed target collapse: source=%d conversion=%d", origins.Load(), rpc.Load()-probeCalls)
	}
	sourceMu.Lock()
	sourceMIME, sourceBytes = "image/png", pngBytes.Bytes()
	sourceMu.Unlock()
	if err = store.PURGE(target, storagev1.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	if err = get("jpeg"); err != nil {
		t.Fatal(err)
	}
	if origins.Load() != 2 || rpc.Load() != probeCalls+3 {
		t.Fatal("PNG to JPEG did not rebind source content version")
	}
}

func TestManagedStartupCannotSilentlyBecomeTransparentProxy(t *testing.T) {
	old := storage.Current()
	defer storage.SetDefault(old)
	config := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", SliceSize: 64, DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()}, Buckets: []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}
	store, err := storage.New(config, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storage.SetDefault(store)
	var calls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unavailable worker", 503) }))
	defer worker.Close()
	for _, kind := range []string{"missing-caching", "disabled-representation", "query-key-disabled", "worker-contract-failure"} {
		t.Run(kind, func(t *testing.T) {
			transformConfig := publicTransformConfig(worker.URL+"/v1/transform", t.TempDir())
			cache := &middlewarev1.Middleware{Name: "caching", Options: map[string]any{"include_query_in_cache_key": true, "representation": map[string]any{"enabled": true}}}
			configs := []*middlewarev1.Middleware{transformConfig, cache}
			switch kind {
			case "missing-caching":
				configs = configs[:1]
			case "disabled-representation":
				cache.Options["representation"] = map[string]any{"enabled": false}
			case "query-key-disabled":
				cache.Options["include_query_in_cache_key"] = false
			}
			before := calls.Load()
			server := &HTTPServer{config: &conf.Bootstrap{Storage: config, Server: &conf.Server{Middleware: configs}}}
			if _, err := server.buildMiddlewareChain(middleware.RoundTripperFunc(func(*http.Request) (*http.Response, error) { t.Error("failed startup reached source"); return nil, nil })); err == nil {
				t.Fatal("invalid transform startup silently accepted")
			}
			if kind != "worker-contract-failure" && calls.Load() != before {
				t.Fatal("RPC preceded local validation")
			}
			if kind == "worker-contract-failure" && calls.Load() == before {
				t.Fatal("missing actual worker startup check")
			}
		})
	}
}
