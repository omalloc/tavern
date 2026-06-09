package caching

import (
	"net/http"
	"strings"
	"testing"
)

func BenchmarkWithPooling(b *testing.B) {
	for i := 0; i < b.N; i++ {
		c := cachingPool.Get().(*Caching)
		c.reset()
	}
}

func TestObjectFlight_PanicRecovery(t *testing.T) {
	g := &ObjectFlightGroup{}
	_, _, err := g.Do("key", 0, func() (*http.Response, error) {
		panic("boom")
	})
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("expected panic error, got %v", err)
	}
}
