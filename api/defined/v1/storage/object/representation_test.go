package object

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/pkg/encoding/cobr"
	jsoncodec "github.com/omalloc/tavern/pkg/encoding/json"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

func representationFixture(t *testing.T) (*Metadata, *Metadata) {
	t.Helper()
	key, err := NewResourceKey("https://Example.com/a%2Fb?q=one&q=two#fragment", "http-v2", "origin-a")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := varycontrol.Capture([]string{"Accept", "X-Empty"}, http.Header{"Accept": {`image/webp;profile="a,b"`}, "X-Empty": {""}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := key.RepresentationID("incarnation", 3, varycontrol.Fingerprint(snapshot), "generation")
	if err != nil {
		t.Fatal(err)
	}
	root := &Metadata{ID: key.ID(), RecordKind: RecordResource, Resource: &ResourceRecord{
		SchemaVersion: RepresentationSchemaVersion, Revision: 9, ResourceID: key.ResourceID(),
		CanonicalURL: key.CanonicalURL(), Namespace: key.Namespace(), Incarnation: "incarnation",
		PurgeEpoch: 3, RevalidateEpoch: 4, ActiveLocation: "disk-a", RoutingEpoch: 5,
		Variants: []VariantRef{{Revision: 9, SelectionKind: SelectionHTTP, VaryFields: []string{"accept", "x-empty"},
			SelectorSnapshot: snapshot, SelectorFingerprint: varycontrol.Fingerprint(snapshot),
			RepresentationID: id, ResponseDate: 100, LastAccess: 110}},
	}}
	response := &Metadata{ID: id.Clone(), RecordKind: RecordRepresentation, BlockSize: 64, Size: 4, Code: 200,
		Headers: http.Header{"Content-Type": {"image/webp"}, "Vary": {"Accept, X-Empty"}},
		Representation: &RepresentationInfo{Revision: 9, StorageSelectorFingerprint: varycontrol.Fingerprint(snapshot), SchemaVersion: RepresentationSchemaVersion, ResourceID: key.ResourceID(),
			Incarnation: "incarnation", PurgeEpoch: 3, ValidatedEpoch: 4, BodyGeneration: "generation",
			State: RepresentationComplete, Kind: RepresentationOrigin},
	}
	response.Chunks.Set(0)
	return root, response
}

func TestResourceRecordCodec(t *testing.T) {
	root, response := representationFixture(t)
	derived := response.Clone()
	derived.Representation.Kind = RepresentationDerived
	derived.Representation.SourceID = response.ID.Clone()
	derived.Representation.SourceVersion = "source-v1"
	derived.Representation.TransformSpec = []byte(`{"schema_version":1,"quality":82}`)
	derived.Representation.PolicyVersion = "images-v1"
	derived.Representation.EncoderVersion = "encoder-v1"
	managed := root.Clone()
	managed.Resource.Variants[0].SelectionKind = SelectionManaged
	managed.Resource.Variants[0].SelectedSourceID = response.ID.Clone()
	managed.Resource.Variants[0].SelectedSourceVersion = "source-v1"
	managed.Resource.Variants[0].TransformFingerprint = strings.Repeat("a", 64)
	for _, codec := range []encoding.Codec{&jsoncodec.JSONCodec{}, &cobr.CborCodec{}} {
		t.Run(codec.Name(), func(t *testing.T) {
			for _, md := range []*Metadata{root, response, managed, derived} {
				data, err := codec.Marshal(md)
				if err != nil {
					t.Fatal(err)
				}
				var decoded Metadata
				if err := codec.Unmarshal(data, &decoded); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(md, &decoded) {
					t.Fatalf("metadata round trip differs:\n%+v\n%+v", md, &decoded)
				}
				if decoded.ID.Hash() != md.ID.Hash() {
					t.Fatal("codec changed physical identity")
				}
			}
			legacy := &Metadata{ID: NewID("http://example.com/plain"), BlockSize: 64, Size: 4, Code: 200}
			legacy.Chunks.Set(0)
			data, err := codec.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Metadata
			if err := codec.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if !decoded.IsLegacy() || !decoded.HasComplete() {
				t.Fatal("old record no longer decodes as a response")
			}
		})
	}
}

func TestResourceRecordClone(t *testing.T) {
	root, response := representationFixture(t)
	clone := root.Clone()
	if !reflect.DeepEqual(root, clone) {
		t.Fatal("clone differs before mutation")
	}
	clone.Resource.Variants[0].VaryFields[0] = "other"
	clone.Resource.Variants[0].SelectorSnapshot.Fields[0].Values[0] = "secret changed"
	clone.Resource.Variants[0].RepresentationID.path = "changed child"
	clone.Resource.ActiveLocation = "changed location"
	clone.ID.path = "changed root"
	if root.Resource.Variants[0].VaryFields[0] != "accept" || root.Resource.Variants[0].SelectorSnapshot.Fields[0].Values[0] == "secret changed" ||
		root.Resource.Variants[0].RepresentationID.Path() == "changed child" || root.ID.Path() == "changed root" || root.Resource.ActiveLocation != "disk-a" {
		t.Fatal("clone aliases nested resource data")
	}
	response.Representation.Kind = RepresentationDerived
	response.Representation.SourceID = NewID("http://source/")
	response.Representation.TransformSpec = []byte(`{"quality":82}`)
	copy := response.Clone()
	copy.Representation.SourceID.path = "other"
	copy.Representation.TransformSpec[0] = '['
	copy.Headers.Set("Content-Type", "image/png")
	copy.Chunks.Clear()
	if response.Representation.SourceID.Path() != "http://source/" || response.Representation.TransformSpec[0] != '{' || response.Headers.Get("Content-Type") != "image/webp" || response.Chunks.Count() != 1 {
		t.Fatal("clone aliases representation data")
	}
}

func TestRepresentationReadability(t *testing.T) {
	root, response := representationFixture(t)
	if root.HasComplete() || root.IsResponse() || !response.HasComplete() {
		t.Fatal("control/response classification")
	}
	for _, state := range []RepresentationState{RepresentationFilling, RepresentationFailed, "unknown"} {
		copy := response.Clone()
		copy.Representation.State = state
		if copy.HasComplete() {
			t.Fatalf("readable %s representation", state)
		}
	}
	copy := response.Clone()
	copy.Representation.SchemaVersion++
	if copy.IsResponse() || copy.HasComplete() {
		t.Fatal("unknown version is readable")
	}
	copy = response.Clone()
	copy.RecordKind = "future-record"
	if copy.IsResponse() || copy.HasComplete() {
		t.Fatal("unknown kind is readable")
	}
	copy = response.Clone()
	copy.BlockSize = 0
	if copy.HasComplete() {
		t.Fatal("zero block size is complete")
	}
	copy = response.Clone()
	copy.Parts.Set(0)
	if copy.HasComplete() {
		t.Fatal("unconfirmed chunk was readable")
	}
	copy = response.Clone()
	copy.Chunks.Clear()
	copy.Chunks.Set(1)
	if copy.HasComplete() {
		t.Fatal("wrong chunk offset is complete")
	}
	root.Resource.SchemaVersion++
	if !root.IsControl() || root.HasComplete() {
		t.Fatal("future control version lost protection")
	}
}

func TestStructuredKey(t *testing.T) {
	key, err := NewResourceKey("HTTPS://EXAMPLE.COM/a%2Fb?x=2&x=1#ignored", "http-v2", "credential-scope")
	if err != nil {
		t.Fatal(err)
	}
	if key.CanonicalURL() != "https://example.com/a%2Fb?x=2&x=1" {
		t.Fatal(key.CanonicalURL())
	}
	otherFragment, _ := NewResourceKey("https://example.com/a%2Fb?x=2&x=1#different", "http-v2", "credential-scope")
	if key.ID().Hash() != otherFragment.ID().Hash() {
		t.Fatal("fragment affects identity")
	}
	for _, tc := range []struct{ url, namespace, scope string }{
		{"https://example.com/a/b?x=2&x=1", "http-v2", "credential-scope"},
		{"https://example.com/a%2Fb?x=1&x=2", "http-v2", "credential-scope"},
		{key.CanonicalURL(), "other", "credential-scope"},
		{key.CanonicalURL(), "http-v2", "other-scope"},
	} {
		other, err := NewResourceKey(tc.url, tc.namespace, tc.scope)
		if err != nil {
			t.Fatal(err)
		}
		if other.ID().Hash() == key.ID().Hash() {
			t.Fatal("distinct resources alias")
		}
	}
	a, _ := NewResourceKey("https://example.com/a", "http-v2", "b")
	b, _ := NewResourceKey("https://example.com/", "http-v2", "ab")
	if a.ResourceID() == b.ResourceID() {
		t.Fatal("ambiguous structured digest")
	}
	snapshot, _ := varycontrol.Capture([]string{"Cookie"}, http.Header{"Cookie": {"secret-cookie"}})
	id, err := key.RepresentationID("inc", 1, varycontrol.Fingerprint(snapshot), "body-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, alternative := range []struct {
		incarnation          string
		epoch                uint64
		selector, generation string
	}{
		{"other-inc", 1, varycontrol.Fingerprint(snapshot), "body-a"},
		{"inc", 2, varycontrol.Fingerprint(snapshot), "body-a"},
		{"inc", 1, strings.Repeat("0", 64), "body-a"},
		{"inc", 1, varycontrol.Fingerprint(snapshot), "body-b"},
	} {
		other, err := key.RepresentationID(alternative.incarnation, alternative.epoch, alternative.selector, alternative.generation)
		if err != nil || id.Hash() == other.Hash() {
			t.Fatal("representation generations or selectors alias", err)
		}
	}
	if id.Path() != key.CanonicalURL() || strings.Contains(id.Ext(), "secret") || strings.Contains(id.Ext(), "credential-scope") {
		t.Fatal("unsafe physical suffix")
	}
	if id.Hash() == NewID(id.Key()).Hash() || key.ID().Hash() == id.Hash() {
		t.Fatal("public URL or control aliases a representation")
	}
	for _, codec := range []encoding.Codec{&jsoncodec.JSONCodec{}, &cobr.CborCodec{}} {
		for _, original := range []*ID{key.ID(), id} {
			data, err := codec.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var decoded ID
			if err := codec.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, &decoded) {
				t.Fatalf("%s changed structured ID", codec.Name())
			}
		}
	}
	for _, raw := range []string{"/relative", "ftp://example.com/a", "https://user:password@example.com/a"} {
		if _, err := NewResourceKey(raw, "http-v2", ""); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
