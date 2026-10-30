package transform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"sync/atomic"
	"testing"
	"time"

	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

func workerCapabilities(webp bool) transformv1.BackendCapabilities {
	caps := transformv1.BackendCapabilities{SchemaVersion: 1, Inputs: []string{"image/jpeg", "image/png"}, Outputs: []transformv1.Codec{{MIME: "image/jpeg", QualityMin: 1, QualityMax: 100}, {MIME: "image/png", Lossless: true, CompressionMin: 0, CompressionMax: 9}}, FlattenAlpha: true, PolicyVersion: "contract-p1", EncoderVersion: "test-worker-e1"}
	if webp {
		caps.Inputs = append(caps.Inputs, "image/webp")
		caps.Outputs = append(caps.Outputs, transformv1.Codec{MIME: "image/webp", QualityMin: 1, QualityMax: 100, Lossless: true})
	}
	for _, input := range caps.Inputs {
		for _, output := range caps.Outputs {
			caps.Conversions = append(caps.Conversions, transformv1.Conversion{InputMIME: input, OutputMIME: output.MIME})
		}
	}
	return caps
}
func testWorkerConfig(endpoint, directory string) HTTPWorkerConfig {
	return HTTPWorkerConfig{Endpoint: endpoint, Capabilities: workerCapabilities(false), Limits: WorkerLimits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxPixels: 1024, Concurrency: 2, QueueSize: 8, Timeout: time.Second, TemporaryDirectory: directory}}
}
func testWorkerInput(t *testing.T) *FileBlob {
	t.Helper()
	fixtures, err := workerProbeImages(false)
	if err != nil {
		t.Fatal(err)
	}
	source, err := SpoolDecoded(context.Background(), bytes.NewReader(fixtures["image/png"]), "identity", t.TempDir(), 1<<20, 1024, ImageInspector{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { source.Close() })
	return source
}
func testJPEGSpec() transformv1.TransformSpec {
	q := 85
	return transformv1.TransformSpec{SchemaVersion: 1, OutputMIME: "image/jpeg", Quality: &q, AlphaPolicy: "reject", AnimationPolicy: "reject", OrientationPolicy: "preserve", ColorPolicy: "preserve"}
}
func directoryEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary outputs leaked: %v", entries)
	}
}

// This worker always encodes actual pixels. Default tests use only Go JPEG/PNG;
// the explicit contract test additionally invokes a locally installed cwebp.
func codecWorker(t *testing.T, cwebp string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		fail := func(err error) { t.Errorf("worker: %v", err); http.Error(w, "bad contract", 400) }
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/v1/transform" || r.Header.Get(VersionHeader) != "1" || r.Header.Get("Accept-Encoding") != "identity" {
			fail(fmt.Errorf("request contract: %s %s %v", r.Method, r.URL, r.Header))
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Tavern-Source-URL") != "" {
			fail(errors.New("client metadata forwarded"))
			return
		}
		encoded, err := base64.RawURLEncoding.DecodeString(r.Header.Get(SpecHeader))
		if err != nil {
			fail(err)
			return
		}
		var spec transformv1.TransformSpec
		if err = json.Unmarshal(encoded, &spec); err != nil {
			fail(err)
			return
		}
		canonical, _ := json.Marshal(spec)
		if !bytes.Equal(canonical, encoded) {
			fail(errors.New("noncanonical spec"))
			return
		}
		if r.Header.Get("Accept") != spec.OutputMIME {
			fail(errors.New("accept differs from spec"))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fail(err)
			return
		}
		if int64(len(body)) != r.ContentLength {
			fail(errors.New("incomplete input"))
			return
		}
		img, format, err := image.Decode(bytes.NewReader(body))
		if err != nil {
			fail(err)
			return
		}
		if r.Header.Get("Content-Type") != "image/"+format {
			fail(errors.New("input MIME differs from bytes"))
			return
		}
		var output bytes.Buffer
		if spec.OutputMIME == "image/jpeg" {
			if spec.AlphaPolicy == "flatten" {
				rgb, err := strconv.ParseUint(strings.TrimPrefix(spec.Background, "#"), 16, 32)
				if err != nil {
					fail(err)
					return
				}
				canvas := image.NewRGBA(img.Bounds())
				draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{byte(rgb >> 16), byte(rgb >> 8), byte(rgb), 255}), image.Point{}, draw.Src)
				draw.Draw(canvas, canvas.Bounds(), img, img.Bounds().Min, draw.Over)
				img = canvas
			}
			err = jpeg.Encode(&output, img, &jpeg.Options{Quality: *spec.Quality})
		} else if spec.OutputMIME == "image/png" {
			level := png.DefaultCompression
			if spec.CompressionLevel != nil && *spec.CompressionLevel == 0 {
				level = png.NoCompression
			}
			err = (&png.Encoder{CompressionLevel: level}).Encode(&output, img)
		} else {
			if cwebp == "" {
				fail(errors.New("WebP encoder not enabled"))
				return
			}
			dir, dirErr := os.MkdirTemp("", "tavern-cwebp-contract-*")
			if dirErr != nil {
				fail(dirErr)
				return
			}
			defer os.RemoveAll(dir)
			inputPath, outputPath := filepath.Join(dir, "input.png"), filepath.Join(dir, "output.webp")
			var input bytes.Buffer
			if err = png.Encode(&input, img); err == nil {
				err = os.WriteFile(inputPath, input.Bytes(), 0600)
			}
			if err != nil {
				fail(err)
				return
			}
			args := []string{"-quiet", "-exact"}
			if spec.Lossless {
				args = append(args, "-lossless")
			} else {
				args = append(args, "-q", strconv.Itoa(*spec.Quality))
			}
			args = append(args, inputPath, "-o", outputPath)
			if log, runErr := exec.CommandContext(r.Context(), cwebp, args...).CombinedOutput(); runErr != nil {
				fail(fmt.Errorf("cwebp: %w %s", runErr, log))
				return
			}
			var data []byte
			data, err = os.ReadFile(outputPath)
			output.Write(data)
		}
		if err != nil {
			fail(err)
			return
		}
		w.Header().Set("Content-Type", spec.OutputMIME)
		w.Header().Set(EncoderHeader, "test-worker-e1")
		w.Write(output.Bytes())
	}
}

func TestHTTPWorkerWireAndOutputOwnership(t *testing.T) {
	server := httptest.NewServer(codecWorker(t, ""))
	defer server.Close()
	dir := t.TempDir()
	config := testWorkerConfig(server.URL+"/v1/transform", dir)
	worker, err := NewHTTPWorker(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if worker.Verified() {
		t.Fatal("unprobed worker was verified")
	}
	config.Capabilities.Outputs[0].MIME = "changed"
	caps := worker.Capabilities()
	caps.Inputs[0] = "changed"
	if worker.Capabilities().Inputs[0] == "changed" {
		t.Fatal("capabilities alias")
	}
	source := testWorkerInput(t)
	output, err := worker.Transform(context.Background(), source, testJPEGSpec())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := output.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if output.Digest() != hex.EncodeToString(digest[:]) || output.Info().Size != int64(len(data)) || output.EncoderVersion() != "test-worker-e1" {
		t.Fatal("invalid output metadata")
	}
	if len(worker.budget.active) != 1 {
		t.Fatal("completed output did not retain permit")
	}
	output.Close()
	output.Close()
	if len(worker.budget.active) != 0 {
		t.Fatal("output leaked concurrency permit")
	}
	directoryEmpty(t, dir)
}

func TestHTTPWorkerRejectsInvalidOutputsAndCleans(t *testing.T) {
	fixtures, _ := workerProbeImages(false)
	tests := []struct {
		name   string
		mutate func(http.ResponseWriter)
		body   []byte
		max    int64
	}{
		{name: "non-200", mutate: func(w http.ResponseWriter) { w.WriteHeader(503) }, body: fixtures["image/jpeg"]},
		{name: "error-page", body: []byte("<html>not an image</html>")},
		{name: "wrong-MIME", mutate: func(w http.ResponseWriter) { w.Header().Set("Content-Type", "image/png") }, body: fixtures["image/jpeg"]},
		{name: "wrong-bytes", body: fixtures["image/png"]},
		{name: "wrong-version", mutate: func(w http.ResponseWriter) { w.Header().Set(EncoderHeader, "other") }, body: fixtures["image/jpeg"]},
		{name: "encoded-output", mutate: func(w http.ResponseWriter) { w.Header().Set("Content-Encoding", "gzip") }, body: fixtures["image/jpeg"]},
		{name: "truncated", body: fixtures["image/jpeg"][:len(fixtures["image/jpeg"])-2]},
		{name: "declared-short", mutate: func(w http.ResponseWriter) { w.Header().Set("Content-Length", "99999") }, body: fixtures["image/jpeg"]},
		{name: "byte-budget", body: fixtures["image/jpeg"], max: 32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set(EncoderHeader, "test-worker-e1")
				if tt.mutate != nil {
					tt.mutate(w)
				}
				w.Write(tt.body)
			}))
			defer server.Close()
			dir := t.TempDir()
			config := testWorkerConfig(server.URL+"/v1/transform", dir)
			if tt.max > 0 {
				config.Limits.MaxOutputBytes = tt.max
			}
			worker, err := NewHTTPWorker(config, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			output, err := worker.Transform(context.Background(), testWorkerInput(t), testJPEGSpec())
			if output != nil {
				output.Close()
			}
			if err == nil {
				t.Fatal("accepted invalid worker output")
			}
			if len(worker.budget.active) != 0 {
				t.Fatal("error leaked permit")
			}
			directoryEmpty(t, dir)
		})
	}
}
func TestHTTPWorkerTimeoutQueueAndRedirect(t *testing.T) {
	source := testWorkerInput(t)
	fixtures, _ := workerProbeImages(false)
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set(EncoderHeader, "test-worker-e1")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer server.Close()
		dir := t.TempDir()
		config := testWorkerConfig(server.URL+"/v1/transform", dir)
		config.Limits.Timeout = 30 * time.Millisecond
		worker, err := NewHTTPWorker(config, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = worker.Transform(context.Background(), source, testJPEGSpec()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout: %v", err)
		}
		if len(worker.budget.active) != 0 {
			t.Fatal("timeout leaked permit")
		}
		directoryEmpty(t, dir)
	})
	t.Run("completed-output-bounds-queue", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set(EncoderHeader, "test-worker-e1")
			w.Write(fixtures["image/jpeg"])
		}))
		defer server.Close()
		config := testWorkerConfig(server.URL+"/v1/transform", t.TempDir())
		config.Limits.Concurrency = 1
		worker, err := NewHTTPWorker(config, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		out, err := worker.Transform(context.Background(), source, testJPEGSpec())
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err = worker.Transform(ctx, source, testJPEGSpec()); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		config.Limits.Timeout = time.Millisecond
		ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err = worker.Transform(ctx, source, testJPEGSpec()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		out.Close()
		if len(worker.budget.active) != 0 || len(worker.budget.queued) != 0 {
			t.Fatal("queue leaked")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var hits atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		worker, err := NewHTTPWorker(testWorkerConfig(server.URL+"/v1/transform", t.TempDir()), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = worker.Transform(context.Background(), source, testJPEGSpec()); !errors.Is(err, ErrWorker) {
			t.Fatal(err)
		}
		if hits.Load() != 0 {
			t.Fatal("worker redirected private source bytes")
		}
	})
}
func TestHTTPWorkerContractVerify(t *testing.T) {
	server := httptest.NewServer(codecWorker(t, ""))
	defer server.Close()
	dir := t.TempDir()
	worker, err := NewHTTPWorker(testWorkerConfig(server.URL+"/v1/transform", dir), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	report, err := worker.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !worker.Verified() || len(report.Cases) != 8 || report.CapabilityFingerprint == "" {
		t.Fatalf("incomplete matrix report: %+v", report)
	}
	directoryEmpty(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = worker.Verify(ctx); err == nil || worker.Verified() {
		t.Fatal("failed verification retained gate")
	}
	directoryEmpty(t, dir)
}
func TestHTTPWorkerRealWebPContract(t *testing.T) {
	if os.Getenv("TAVERN_TEST_CWEBP") != "1" {
		t.Skip("explicit local JPEG/PNG/WebP encoder contract; set TAVERN_TEST_CWEBP=1")
	}
	cwebp, err := exec.LookPath("cwebp")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(cwebp, "-version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual WebP encoder: %s", version)
	server := httptest.NewServer(codecWorker(t, cwebp))
	defer server.Close()
	dir := t.TempDir()
	config := testWorkerConfig(server.URL+"/v1/transform", dir)
	config.Capabilities = workerCapabilities(true)
	config.Limits.Timeout = 10 * time.Second
	worker, err := NewHTTPWorker(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	report, err := worker.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !worker.Verified() || len(report.Cases) != 24 {
		t.Fatalf("missing matrix/quality/alpha checks: %+v", report)
	}
	directoryEmpty(t, dir)
}

func TestHTTPWorkerContractRejectsIgnoredQuality(t *testing.T) {
	fixtures, _ := workerProbeImages(false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set(EncoderHeader, "test-worker-e1")
		w.Write(fixtures["image/jpeg"])
	}))
	defer server.Close()
	config := testWorkerConfig(server.URL+"/v1/transform", t.TempDir())
	config.Capabilities.Inputs = []string{"image/jpeg"}
	config.Capabilities.Outputs = config.Capabilities.Outputs[:1]
	config.Capabilities.Conversions = []transformv1.Conversion{{InputMIME: "image/jpeg", OutputMIME: "image/jpeg"}}
	worker, err := NewHTTPWorker(config, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.Verify(context.Background()); !errors.Is(err, ErrWorker) || worker.Verified() {
		t.Fatalf("ignored quality passed gate: %v", err)
	}
	directoryEmpty(t, config.Limits.TemporaryDirectory)
}
func TestHTTPWorkerRejectsDuplicateContractFields(t *testing.T) {
	fixtures, _ := workerProbeImages(false)
	for _, header := range []string{"Content-Type", EncoderHeader, "Content-Encoding"} {
		t.Run(header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set(EncoderHeader, "test-worker-e1")
				value := w.Header().Get(header)
				if header == "Content-Encoding" {
					value = "identity"
					w.Header().Set(header, value)
				}
				w.Header().Add(header, value)
				w.Write(fixtures["image/jpeg"])
			}))
			defer server.Close()
			config := testWorkerConfig(server.URL+"/v1/transform", t.TempDir())
			worker, err := NewHTTPWorker(config, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = worker.Transform(context.Background(), testWorkerInput(t), testJPEGSpec()); !errors.Is(err, ErrWorker) {
				t.Fatal(err)
			}
			directoryEmpty(t, config.Limits.TemporaryDirectory)
		})
	}
}

func TestHTTPWorkerRejectsUnverifiedMetadata(t *testing.T) {
	fixtures, _ := workerProbeImages(false)
	data := fixtures["image/jpeg"]
	segment := []byte{0xff, 0xe1, 0, 10, 'E', 'x', 'i', 'f', 0, 0, 'I', 'I'}
	data = append(append(append([]byte{}, data[:2]...), segment...), data[2:]...)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set(EncoderHeader, "test-worker-e1")
		w.Write(data)
	}))
	defer server.Close()
	worker, err := NewHTTPWorker(testWorkerConfig(server.URL+"/v1/transform", t.TempDir()), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	source, err := SpoolDecoded(context.Background(), bytes.NewReader(data), "identity", t.TempDir(), 1<<20, 1024, ImageInspector{})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err = worker.Transform(context.Background(), source, testJPEGSpec()); !errors.Is(err, ErrUnsupportedSource) || calls.Load() != 0 {
		t.Fatal("metadata source sent to unverified preservation backend", err)
	}
	if _, err = worker.Transform(context.Background(), testWorkerInput(t), testJPEGSpec()); !errors.Is(err, ErrWorker) || calls.Load() != 1 {
		t.Fatal("worker introduced unverified metadata", err)
	}
}
