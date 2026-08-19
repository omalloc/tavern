package http

import (
	stdhttp "net/http"
	"testing"
)

func TestHeaderFingerprintIsStable(t *testing.T) {
	first := stdhttp.Header{
		"User-Agent":      {"test"},
		"Accept-Encoding": {"gzip"},
	}
	second := stdhttp.Header{
		"accept-encoding": {"gzip"},
		"user-agent":      {"test"},
	}
	if HeaderFingerprint(first) != HeaderFingerprint(second) {
		t.Fatal("equivalent header maps produced different fingerprints")
	}
}

func TestHeaderFingerprintIsolatesRepresentations(t *testing.T) {
	gzip := stdhttp.Header{"Accept-Encoding": {"gzip"}}
	brotli := stdhttp.Header{"Accept-Encoding": {"br"}}
	if HeaderFingerprint(gzip) == HeaderFingerprint(brotli) {
		t.Fatal("different representation headers produced the same fingerprint")
	}
}

func TestHeaderFingerprintEncodesFieldBoundaries(t *testing.T) {
	first := stdhttp.Header{"A": {"b"}, "C": {"d"}}
	second := stdhttp.Header{"A": {"b", "c", "d"}}
	if HeaderFingerprint(first) == HeaderFingerprint(second) {
		t.Fatal("different header structures produced the same fingerprint")
	}
}
