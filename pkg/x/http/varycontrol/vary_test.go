package varycontrol

import (
	"net/http"
	"testing"
)

func TestClean(t *testing.T) {
	got := Clean(" User-Agent, ACCEPT-Encoding", "user-agent", ", Accept-Language ")
	want := Key{"accept-encoding", "accept-language", "user-agent"}
	if !got.Compare(want) {
		t.Fatalf("Clean() = %v, want %v", got, want)
	}
}

func TestCleanWildcardCannotBeIgnored(t *testing.T) {
	ignore := map[string]struct{}{"accept": {}, "*": {}}
	got := CleanWithIgnore(ignore, "Accept, *")
	if !got.Compare(Key{"*"}) || !got.Wildcard() {
		t.Fatalf("CleanWithIgnore() = %v, want wildcard", got)
	}
}

func TestSelectorDigestPresenceAndFraming(t *testing.T) {
	key := Key{"x-test"}
	missing := make(http.Header)
	empty := http.Header{"X-Test": []string{""}}
	if key.SelectorDigest(missing) == key.SelectorDigest(empty) {
		t.Fatal("missing and explicitly empty headers produced the same digest")
	}

	a := http.Header{"X-Test": []string{"a&b=c,雪"}}
	b := http.Header{"X-Test": []string{"a", "b=c,雪"}}
	if key.SelectorDigest(a) == key.SelectorDigest(b) {
		t.Fatal("ambiguous header encodings produced the same digest")
	}
}

func TestSelectorDigestPreservesGenericOrder(t *testing.T) {
	key := Key{"accept-language"}
	a := http.Header{"Accept-Language": []string{"en, zh"}}
	b := http.Header{"Accept-Language": []string{"zh, en"}}
	if key.SelectorDigest(a) == key.SelectorDigest(b) {
		t.Fatal("ordered field members were incorrectly sorted")
	}
}

func TestAcceptEncodingNormalization(t *testing.T) {
	key := Key{"accept-encoding"}
	a := http.Header{"Accept-Encoding": []string{"GZip ; Q=0.5, br"}}
	b := http.Header{"Accept-Encoding": []string{"gzip;q=0.5,br"}}
	if key.SelectorDigest(a) != key.SelectorDigest(b) {
		t.Fatal("equivalent Accept-Encoding OWS/case did not normalize")
	}
	c := http.Header{"Accept-Encoding": []string{"gzip;q=0.7,br"}}
	if key.SelectorDigest(a) == key.SelectorDigest(c) {
		t.Fatal("Accept-Encoding qvalue was discarded")
	}
}
