package caching

import (
	"net/http"
	"strings"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

func TestVariantObjectKeyOpaque(t *testing.T) {
	headers := http.Header{"Cookie": []string{"session=secret"}}
	selector := varycontrol.Key{"cookie"}.SelectorDigest(headers)
	key := variantObjectKey("schema", selector, []object.VariantDimension{
		{Namespace: "image", Value: "webp;q=0.7"},
	})
	if !strings.HasPrefix(key, "vk2.") {
		t.Fatalf("variant key %q has no v2 prefix", key)
	}
	if strings.Contains(key, "secret") || strings.Contains(key, "webp") {
		t.Fatalf("variant key leaks selector or dimension values: %q", key)
	}
}

func TestCollapsedFlightKeySeparatesHeaders(t *testing.T) {
	a, _ := http.NewRequest(http.MethodGet, "http://example.test/a", nil)
	b := a.Clone(a.Context())
	a.Header.Set("Accept-Language", "en")
	b.Header.Set("Accept-Language", "zh")
	if collapsedFlightKey("base", a) == collapsedFlightKey("base", b) {
		t.Fatal("different end-to-end headers collapsed into one flight")
	}
	b.Header.Set("Accept-Language", "en")
	a.Header.Set("X-Request-ID", "one")
	b.Header.Set("X-Request-ID", "two")
	if collapsedFlightKey("base", a) != collapsedFlightKey("base", b) {
		t.Fatal("trace headers should not split collapsed flights")
	}
}

func TestIgnoreKeysCaseInsensitive(t *testing.T) {
	v := NewVaryProcessor(WithVaryIgnoreKeys(" User-Agent ", "*"))
	if _, ok := v.varyIgnoreKey["user-agent"]; !ok {
		t.Fatal("ignore key was not normalized")
	}
	if _, ok := v.varyIgnoreKey["*"]; ok {
		t.Fatal("wildcard must not be ignored")
	}
}
