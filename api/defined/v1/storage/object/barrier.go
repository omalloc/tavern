package object

import "slices"

// BarrierCatalog belongs to one control-store owner and covers every namespace
// and isolation scope managed by that owner. It is never an HTTP response or an
// ordinary LRU entry. Prefix records cannot be expired or silently evicted.
type BarrierCatalog struct {
	SchemaVersion       uint16          `json:"schema_version" yaml:"schema_version"`
	Incarnation         string          `json:"incarnation" yaml:"incarnation"`
	TopologyFingerprint string          `json:"topology_fingerprint,omitempty" yaml:"topology_fingerprint,omitempty"`
	Revision            uint64          `json:"revision" yaml:"revision"`
	Entries             []PrefixBarrier `json:"entries" yaml:"entries"`
	QuerylessNamespaces []string        `json:"queryless_namespaces,omitempty" yaml:"queryless_namespaces,omitempty"`
}

type PrefixBarrier struct {
	CanonicalPrefix string `json:"canonical_prefix" yaml:"canonical_prefix"`
	Exact           bool   `json:"exact,omitempty" yaml:"exact,omitempty"`
	Namespace       string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	HardEpoch       uint64 `json:"hard_epoch" yaml:"hard_epoch"`
	SoftEpoch       uint64 `json:"soft_epoch" yaml:"soft_epoch"`
}

func (b *BarrierCatalog) Clone() *BarrierCatalog {
	if b == nil {
		return nil
	}
	out := *b
	out.Entries = slices.Clone(b.Entries)
	out.QuerylessNamespaces = slices.Clone(b.QuerylessNamespaces)
	return &out
}

func BarrierCatalogID() *ID {
	return NewVirtualID("http://tavern.invalid/representation-barriers", representationKeyPrefix+string(RecordBarriers)+"/"+structuredDigest("barriers", "catalog"))
}

// ValidControl checks the storage envelope. The coordinator additionally
// validates identity, epochs, supported schemas and prefix semantics.
func (m *Metadata) ValidControl() bool {
	if m == nil || !m.ID.IsControl() || m.Representation != nil || len(m.Headers) != 0 || len(m.VirtualKey) != 0 ||
		m.Code != 0 || m.Size != 0 || m.BlockSize != 0 || m.Chunks.Count() != 0 || m.Parts.Count() != 0 || m.Flags != FlagCache {
		return false
	}
	switch m.RecordKind {
	case RecordResource:
		return !m.ID.IsBarrierControl() && m.Resource != nil && m.Resource.SchemaVersion == RepresentationSchemaVersion && m.Barriers == nil
	case RecordBarriers:
		return m.ID.Hash() == BarrierCatalogID().Hash() && m.Resource == nil && m.Barriers != nil && m.Barriers.SchemaVersion == RepresentationSchemaVersion
	default:
		return false
	}
}
