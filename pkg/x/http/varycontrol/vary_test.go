package varycontrol

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
)

func TestVaryDataPreservesOpaqueHeaderBytes(t *testing.T) {
	key := Clean("X-Mode")
	seen := make(map[string]string)
	for _, value := range []string{"\x80", "\x81", "\xff", "\ufffd", "gA==", "plain", "\u00a0plain"} {
		raw := "GET / HTTP/1.1\r\nHost: example.com\r\nX-Mode: " + value + "\r\n\r\n"
		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
		if err != nil {
			t.Fatalf("parse header %q: %v", value, err)
		}
		data := key.VaryData(req.Header)
		if data == `v2:[{"name":"x-mode","present":true,"value":"gA=="}]` {
			t.Fatal("encoded value collided with an older plaintext v2 key")
		}
		if previous, ok := seen[data]; ok {
			t.Fatalf("headers %q and %q share Vary key %s", previous, value, data)
		}
		seen[data] = value
		if padded := key.VaryData(http.Header{"X-Mode": {" \t" + value + "\t "}}); padded != data {
			t.Fatalf("HTTP optional whitespace changed key for %q", value)
		}
	}
}

func TestCleanCanonicalizesAndDeduplicatesFieldNames(t *testing.T) {
	got := Clean("accept-encoding, User-Agent", "ACCEPT-ENCODING")
	want := Key{"Accept-Encoding", "User-Agent"}
	if !got.Compare(want) {
		t.Fatalf("Clean() = %#v, want %#v", got, want)
	}
}

func TestVaryDataDistinguishesAbsentAndEmptyFields(t *testing.T) {
	key := Clean("X-Mode")
	absent := key.VaryData(http.Header{})
	empty := key.VaryData(http.Header{"X-Mode": {""}})
	if absent == empty {
		t.Fatalf("absent and empty headers produced the same key %q", absent)
	}
}

func TestVaryDataPreservesValueOrder(t *testing.T) {
	key := Clean("Accept-Encoding")
	first := key.VaryData(http.Header{"Accept-Encoding": {"gzip, br"}})
	second := key.VaryData(http.Header{"Accept-Encoding": {"br, gzip"}})
	if first == second {
		t.Fatalf("ordered header values produced the same key %q", first)
	}
}

func TestVaryDataHasNoDelimiterCollision(t *testing.T) {
	key := Clean("A, B")
	firstHeader := http.Header{"A": {"x&B=y"}, "B": {"z"}}
	secondHeader := http.Header{"A": {"x"}, "B": {"y&B=z"}}

	if key.LegacyVaryData(firstHeader) != key.LegacyVaryData(secondHeader) {
		t.Fatal("test setup does not reproduce the legacy delimiter collision")
	}
	if key.VaryData(firstHeader) == key.VaryData(secondHeader) {
		t.Fatal("v2 Vary data retained the legacy delimiter collision")
	}
}

func TestWildcard(t *testing.T) {
	if !Clean("*").HasWildcard() {
		t.Fatal("Vary wildcard was not detected")
	}
}
