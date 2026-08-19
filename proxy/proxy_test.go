package proxy

import (
	"net/http"
	"testing"
)

func TestOnceKeyIncludesRepresentationHeaders(t *testing.T) {
	gzip, _ := http.NewRequest(http.MethodGet, "http://example.com/object", nil)
	gzip.Header.Set("Accept-Encoding", "gzip")
	brotli := gzip.Clone(gzip.Context())
	brotli.Header = gzip.Header.Clone()
	brotli.Header.Set("Accept-Encoding", "br")

	if onceKey(gzip) == onceKey(brotli) {
		t.Fatal("requests for different representations share a proxy collapse key")
	}
	if onceKey(gzip) != onceKey(gzip.Clone(gzip.Context())) {
		t.Fatal("equivalent requests have different proxy collapse keys")
	}
}
