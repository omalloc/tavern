package varycontrol

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func mustCapture(t *testing.T, fields []string, header http.Header) Snapshot {
	t.Helper()
	s, err := Capture(fields, header)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSnapshot(t *testing.T) {
	header := http.Header{"Accept": {`image/webp;profile="a,b", image/jpeg`}, "X-Order": {"second", "first"}}
	before := header.Clone()
	snapshot := mustCapture(t, []string{"X-ORDER", "Accept", "X-Missing", "accept"}, header)
	if !reflect.DeepEqual(header, before) || !Match(snapshot, header) {
		t.Fatal("capture modified the request or did not match")
	}
	if got := snapshot.Fields[0].Values[0]; got != before.Get("Accept") {
		t.Fatalf("changed quoted commas: %q", got)
	}
	header["Accept"][0] = "changed"
	if snapshot.Fields[0].Values[0] != before.Get("Accept") || Match(snapshot, header) {
		t.Fatal("snapshot aliases header values")
	}
}

func TestVaryMatch(t *testing.T) {
	cases := []struct {
		name            string
		stored, request http.Header
		match           bool
	}{
		{"missing", http.Header{}, http.Header{}, true},
		{"missing versus empty", http.Header{}, http.Header{"X-Test": {""}}, false},
		{"present no values", http.Header{"X-Test": nil}, http.Header{}, false},
		{"ordered lines", http.Header{"X-Test": {"a", "b"}}, http.Header{"X-Test": {"b", "a"}}, false},
		{"commas retain order", http.Header{"X-Test": {"a,b"}}, http.Header{"X-Test": {"b,a"}}, false},
		{"opaque case", http.Header{"X-Test": {"ABC"}}, http.Header{"X-Test": {"abc"}}, false},
		{"noncanonical field name", http.Header{"x-test": {"a"}}, http.Header{"X-Test": {"a"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := mustCapture(t, []string{"X-Test"}, tc.stored)
			if got := Match(snapshot, tc.request); got != tc.match {
				t.Fatalf("Match = %t; want %t", got, tc.match)
			}
		})
	}
	s := mustCapture(t, []string{"X-Test"}, http.Header{})
	s.Version++
	if Match(s, http.Header{}) {
		t.Fatal("unknown snapshot version matched")
	}
}

func TestFingerprint(t *testing.T) {
	fields := []string{"a", "c"}
	left := mustCapture(t, fields, http.Header{"A": {"b&c=d"}, "C": {"e"}})
	right := mustCapture(t, fields, http.Header{"A": {"b"}, "C": {"d&c=e"}})
	// Both old concatenated keys were a=b&c=d&c=e.
	if Fingerprint(left) == Fingerprint(right) {
		t.Fatal("ambiguous field boundaries")
	}
	missing := mustCapture(t, []string{"A"}, http.Header{})
	empty := mustCapture(t, []string{"A"}, http.Header{"A": {""}})
	if Fingerprint(missing) == Fingerprint(empty) {
		t.Fatal("missing and empty have the same fingerprint")
	}
	equivalent := mustCapture(t, []string{"C", "A", "a"}, http.Header{"C": {"e"}, "A": {"b&c=d"}})
	if Fingerprint(left) != Fingerprint(equivalent) || len(Fingerprint(left)) != 64 {
		t.Fatal("fingerprint is not canonical SHA-256")
	}
	equivalent.Version++
	if Fingerprint(left) == Fingerprint(equivalent) {
		t.Fatal("fingerprint does not include the version")
	}
}

func TestSnapshotBudget(t *testing.T) {
	// Encoding for one present, single-value field x: 2+4+4+1+1+4+4.
	const overhead = 20
	header := http.Header{"X": {strings.Repeat("a", MaxSnapshotBytes-overhead)}}
	if _, err := Capture([]string{"x"}, header); err != nil {
		t.Fatalf("at budget: %v", err)
	}
	header["X"][0] += "a"
	if _, err := Capture([]string{"x"}, header); !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("over budget: %v", err)
	}
	if _, err := Capture([]string{"*"}, nil); !errors.Is(err, ErrWildcard) {
		t.Fatalf("wildcard capture: %v", err)
	}
}

func FuzzSnapshot(f *testing.F) {
	f.Add("X-Test", "a=b&c=d", "first,second")
	f.Add("Accept", `image/webp;profile="a,b"`, "image/jpeg")
	f.Fuzz(func(t *testing.T, field, first, second string) {
		header := http.Header{"X-Test": {first, second}, "Accept": {first}}
		before := header.Clone()
		snapshot, err := Capture([]string{field}, header)
		if !reflect.DeepEqual(header, before) {
			t.Fatal("capture mutated request")
		}
		again, err2 := Capture([]string{field}, header)
		if !errors.Is(err, err2) || !reflect.DeepEqual(snapshot, again) {
			t.Fatal("nondeterministic capture")
		}
		if err == nil && (!Match(snapshot, header) || Fingerprint(snapshot) != Fingerprint(again)) {
			t.Fatal("captured snapshot does not match")
		}
	})
}
