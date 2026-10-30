package object

import "testing"

func TestManagedIdentityBindsSourceSpecificationAndVersions(t *testing.T) {
	key, _ := NewResourceKey("https://example.com/source", "managed-v2", "origin")
	source, _ := key.RepresentationID("inc", 0, TransformFingerprint(nil, "", ""), "source")
	base := TransformFingerprint([]byte(`{"mime":"image/webp"}`), "p1", "e1")
	one, err := ManagedSelectorFingerprint(source, "v1", base)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []string{TransformFingerprint([]byte(`{"mime":"image/jpeg"}`), "p1", "e1"), TransformFingerprint([]byte(`{"mime":"image/webp"}`), "p2", "e1"), TransformFingerprint([]byte(`{"mime":"image/webp"}`), "p1", "e2")} {
		other, err := ManagedSelectorFingerprint(source, "v1", spec)
		if err != nil || other == one {
			t.Fatal("policy/specification/encoder aliased", err)
		}
	}
	other, _ := ManagedSelectorFingerprint(source, "v2", base)
	if other == one {
		t.Fatal("source version aliased")
	}
	if _, err := ManagedSelectorFingerprint(nil, "v1", base); err == nil {
		t.Fatal("missing source accepted")
	}
}
