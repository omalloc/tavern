package object

import (
	"encoding/json"
	"slices"

	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

const RepresentationSchemaVersion uint16 = 2

type RecordKind string
type SelectionKind string
type RepresentationKind string
type RepresentationState string

const (
	RecordLegacy           RecordKind          = "legacy"
	RecordResource         RecordKind          = "resource"
	RecordRepresentation   RecordKind          = "representation"
	SelectionHTTP          SelectionKind       = "http"
	SelectionManaged       SelectionKind       = "managed"
	RepresentationOrigin   RepresentationKind  = "origin"
	RepresentationDerived  RepresentationKind  = "derived"
	RepresentationFilling  RepresentationState = "filling"
	RepresentationComplete RepresentationState = "complete"
	RepresentationFailed   RepresentationState = "failed"
)

// ResourceRecord is a control record, never an HTTP response. Revision advances
// on each index update; incarnation changes when a resource is reconstructed.
type ResourceRecord struct {
	SchemaVersion   uint16       `json:"schema_version" yaml:"schema_version"`
	Revision        uint64       `json:"revision" yaml:"revision"`
	ResourceID      string       `json:"resource_id" yaml:"resource_id"`
	CanonicalURL    string       `json:"canonical_url" yaml:"canonical_url"`
	Namespace       string       `json:"namespace" yaml:"namespace"`
	Incarnation     string       `json:"incarnation" yaml:"incarnation"`
	PurgeEpoch      uint64       `json:"purge_epoch" yaml:"purge_epoch"`
	RevalidateEpoch uint64       `json:"revalidate_epoch" yaml:"revalidate_epoch"`
	ActiveLocation  string       `json:"active_location" yaml:"active_location"`
	RoutingEpoch    uint64       `json:"routing_epoch" yaml:"routing_epoch"`
	Variants        []VariantRef `json:"variants" yaml:"variants"`
}

type VariantRef struct {
	Revision              uint64               `json:"revision,omitempty" yaml:"revision,omitempty"`
	SelectionKind         SelectionKind        `json:"selection_kind" yaml:"selection_kind"`
	VaryFields            []string             `json:"vary_fields" yaml:"vary_fields"`
	SelectorSnapshot      varycontrol.Snapshot `json:"selector_snapshot" yaml:"selector_snapshot"`
	SelectorFingerprint   string               `json:"selector_fingerprint" yaml:"selector_fingerprint"`
	TransformFingerprint  string               `json:"transform_fingerprint,omitempty" yaml:"transform_fingerprint,omitempty"`
	SelectedSourceID      *ID                  `json:"selected_source_id,omitempty" yaml:"selected_source_id,omitempty"`
	SelectedSourceVersion string               `json:"selected_source_version,omitempty" yaml:"selected_source_version,omitempty"`
	RepresentationID      *ID                  `json:"representation_id" yaml:"representation_id"`
	ResponseDate          int64                `json:"response_date" yaml:"response_date"`
	LastAccess            int64                `json:"last_access" yaml:"last_access"`
}

type RepresentationInfo struct {
	Revision       uint64 `json:"revision,omitempty" yaml:"revision,omitempty"`
	SchemaVersion  uint16 `json:"schema_version" yaml:"schema_version"`
	ResourceID     string `json:"resource_id" yaml:"resource_id"`
	Incarnation    string `json:"incarnation" yaml:"incarnation"`
	PurgeEpoch     uint64 `json:"purge_epoch" yaml:"purge_epoch"`
	ValidatedEpoch uint64 `json:"validated_epoch" yaml:"validated_epoch"`
	BodyGeneration string `json:"body_generation" yaml:"body_generation"`
	// The selector used when the immutable body ID was allocated. A 304 may
	// change the logical selector without moving or rewriting those bytes.
	StorageSelectorFingerprint string              `json:"storage_selector_fingerprint,omitempty" yaml:"storage_selector_fingerprint,omitempty"`
	State                      RepresentationState `json:"state" yaml:"state"`
	Kind                       RepresentationKind  `json:"kind" yaml:"kind"`
	SourceID                   *ID                 `json:"source_id,omitempty" yaml:"source_id,omitempty"`
	SourceVersion              string              `json:"source_version,omitempty" yaml:"source_version,omitempty"`
	// Normalized transform specifications are opaque to the storage model.
	// The transform package will own their interpretation and validation.
	TransformSpec  json.RawMessage `json:"transform_spec,omitempty" yaml:"transform_spec,omitempty"`
	PolicyVersion  string          `json:"policy_version,omitempty" yaml:"policy_version,omitempty"`
	EncoderVersion string          `json:"encoder_version,omitempty" yaml:"encoder_version,omitempty"`
}

func (r *ResourceRecord) Clone() *ResourceRecord {
	if r == nil {
		return nil
	}
	out := *r
	out.Variants = slices.Clone(r.Variants)
	for i := range r.Variants {
		out.Variants[i] = r.Variants[i].Clone()
	}
	return &out
}

func (v VariantRef) Clone() VariantRef {
	v.VaryFields = slices.Clone(v.VaryFields)
	v.SelectorSnapshot.Fields = slices.Clone(v.SelectorSnapshot.Fields)
	for i := range v.SelectorSnapshot.Fields {
		v.SelectorSnapshot.Fields[i].Values = slices.Clone(v.SelectorSnapshot.Fields[i].Values)
	}
	v.SelectedSourceID = v.SelectedSourceID.Clone()
	v.RepresentationID = v.RepresentationID.Clone()
	return v
}

func (r *RepresentationInfo) Clone() *RepresentationInfo {
	if r == nil {
		return nil
	}
	out := *r
	out.SourceID = r.SourceID.Clone()
	out.TransformSpec = slices.Clone(r.TransformSpec)
	return &out
}

func (m *Metadata) IsLegacy() bool {
	return m != nil && (m.RecordKind == "" || m.RecordKind == RecordLegacy) &&
		m.Resource == nil && m.Representation == nil
}

// IsControl also protects unknown future versions of a resource record.
func (m *Metadata) IsControl() bool {
	return m != nil && (m.RecordKind == RecordResource || m.Resource != nil)
}

func (m *Metadata) IsResponse() bool {
	if m.IsLegacy() {
		return true
	}
	return m != nil && m.RecordKind == RecordRepresentation && m.Resource == nil &&
		m.Representation != nil && m.Representation.SchemaVersion == RepresentationSchemaVersion &&
		(m.Representation.Kind == RepresentationOrigin || m.Representation.Kind == RepresentationDerived)
}
