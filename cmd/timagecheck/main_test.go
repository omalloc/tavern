package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"github.com/omalloc/tavern/transform"
	"gopkg.in/yaml.v3"
)

func TestExplicitWorkerCheckUsesConfiguredEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoded, err := base64.RawURLEncoding.DecodeString(r.Header.Get(transform.SpecHeader))
		if err != nil {
			t.Error(err)
			return
		}
		var spec transformv1.TransformSpec
		if err = json.Unmarshal(encoded, &spec); err != nil {
			t.Error(err)
			return
		}
		img, _, err := image.Decode(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var output bytes.Buffer
		if err = jpeg.Encode(&output, img, &jpeg.Options{Quality: *spec.Quality}); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set(transform.EncoderHeader, "test-e1")
		w.Write(output.Bytes())
	}))
	defer server.Close()
	dir := t.TempDir()
	config := transform.HTTPWorkerConfig{Endpoint: server.URL + "/v1/transform", Capabilities: transformv1.BackendCapabilities{SchemaVersion: 1, Inputs: []string{"image/jpeg"}, Outputs: []transformv1.Codec{{MIME: "image/jpeg", QualityMin: 1, QualityMax: 100}}, Conversions: []transformv1.Conversion{{InputMIME: "image/jpeg", OutputMIME: "image/jpeg"}}, PolicyVersion: "test-p1", EncoderVersion: "test-e1"}, Limits: transform.WorkerLimits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxPixels: 1024, Concurrency: 1, QueueSize: 2, Timeout: time.Second, TemporaryDirectory: dir}}
	data, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worker.yaml")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = check(path, time.Second, &output); err != nil {
		t.Fatal(err)
	}
	var report transform.ContractReport
	if err = json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.EncoderVersion != "test-e1" || len(report.Cases) != 2 || report.CapabilityFingerprint == "" {
		t.Fatalf("incomplete report: %+v", report)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("probe files leaked")
	}
	if err = os.WriteFile(path, append(data, []byte("unknown_field: true\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = check(path, time.Second, &output); err == nil {
		t.Fatal("unknown config accepted")
	}
	if err = check("", time.Second, &output); err == nil {
		t.Fatal("missing config accepted")
	}
}
