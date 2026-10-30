package transform

import (
	"net/http"
	"testing"
)

func TestOriginalContentCodingSelection(t *testing.T) {
	for _, tt := range []struct {
		raw, coding string
		want        bool
	}{
		{"identity", "gzip", false}, {"gzip", "identity", true}, {"gzip;q=0.5, *;q=0", "identity", false}, {"*;q=0, identity;q=1", "", true}, {"gzip;q=0, *;q=1", "gzip", false}, {"gzip;q=1, gzip;q=0", "gzip", false}, {"", "gzip", false}, {"", "identity", true}, {"gzip;q=NaN", "identity", false},
	} {
		if got := EncodingAccepted(http.Header{"Accept-Encoding": {tt.raw}}, tt.coding); got != tt.want {
			t.Fatalf("%q %q got=%v", tt.raw, tt.coding, got)
		}
	}
	if !EncodingAccepted(nil, "br") {
		t.Fatal("absent field accepts any coding")
	}
}
