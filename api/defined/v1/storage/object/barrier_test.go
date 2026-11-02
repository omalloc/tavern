package object

import "testing"

func TestBarrierControlEnvelopeAndClone(t *testing.T) {
	md := &Metadata{ID: BarrierCatalogID(), RecordKind: RecordBarriers, Barriers: &BarrierCatalog{
		SchemaVersion: RepresentationSchemaVersion, Incarnation: "catalog", Revision: 1,
		QuerylessNamespaces: []string{"queryless"},
		Entries:             []PrefixBarrier{{CanonicalPrefix: "https://example.com/img/", HardEpoch: 1}},
	}}
	if !md.ID.IsControl() || !md.ValidControl() || md.IsResponse() || md.IsLegacy() || md.HasComplete() {
		t.Fatal("barrier treated as a response")
	}
	copy := md.Clone()
	copy.Barriers.Entries[0].HardEpoch = 2
	copy.Barriers.Incarnation = "other"
	copy.Barriers.QuerylessNamespaces[0] = "other"
	if md.Barriers.Entries[0].HardEpoch != 1 || md.Barriers.Incarnation != "catalog" || md.Barriers.QuerylessNamespaces[0] != "queryless" {
		t.Fatal("barrier clone aliased control history")
	}
	md.Barriers.SchemaVersion++
	if !md.IsControl() || md.ValidControl() || md.IsResponse() {
		t.Fatal("unknown barrier version escaped protection")
	}
}
