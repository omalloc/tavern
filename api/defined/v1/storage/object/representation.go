package object

import (
	"encoding/json"
	"math"
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
	RecordBarriers         RecordKind          = "barriers"
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
	SchemaVersion      uint16       `json:"schema_version" yaml:"schema_version"`
	Revision           uint64       `json:"revision" yaml:"revision"`
	ResourceID         string       `json:"resource_id" yaml:"resource_id"`
	CanonicalURL       string       `json:"canonical_url" yaml:"canonical_url"`
	Namespace          string       `json:"namespace" yaml:"namespace"`
	Incarnation        string       `json:"incarnation" yaml:"incarnation"`
	PurgeEpoch         uint64       `json:"purge_epoch" yaml:"purge_epoch"`
	RevalidateEpoch    uint64       `json:"revalidate_epoch" yaml:"revalidate_epoch"`
	ActiveLocation     string       `json:"active_location" yaml:"active_location"`
	RoutingEpoch       uint64       `json:"routing_epoch" yaml:"routing_epoch"`
	PrefixHardEpoch    uint64       `json:"prefix_hard_epoch,omitempty" yaml:"prefix_hard_epoch,omitempty"`
	PrefixSoftEpoch    uint64       `json:"prefix_soft_epoch,omitempty" yaml:"prefix_soft_epoch,omitempty"`
	BarrierIncarnation string       `json:"barrier_incarnation,omitempty" yaml:"barrier_incarnation,omitempty"`
	Variants           []VariantRef `json:"variants" yaml:"variants"`
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
	// SourceBoundTTL derives freshness only from a current validation of exactly
	// the selected source body version. It never grants independent lifetime.
	SourceBoundTTL bool   `json:"source_bound_ttl,omitempty" yaml:"source_bound_ttl,omitempty"`
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
	Image          *ImageInfo      `json:"image,omitempty" yaml:"image,omitempty"`
}

// ImageInfo describes bounded inspection of a complete, decoded source body.
// It belongs to one immutable body generation and survives a validating 304.
type ImageInfo struct {
	SchemaVersion    uint16 `json:"schema_version" yaml:"schema_version"`
	InspectorVersion string `json:"inspector_version" yaml:"inspector_version"`
	DecodedDigest    string `json:"decoded_digest" yaml:"decoded_digest"`
	MIME             string `json:"mime" yaml:"mime"`
	DecodedSize      int64  `json:"decoded_size" yaml:"decoded_size"`
	Width            int    `json:"width" yaml:"width"`
	Height           int    `json:"height" yaml:"height"`
	HasAlpha         bool   `json:"has_alpha" yaml:"has_alpha"`
	Animated         bool   `json:"animated" yaml:"animated"`
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
	if r.Image != nil {
		image := *r.Image
		out.Image = &image
	}
	return &out
}

func (m *Metadata) IsLegacy() bool {
	return m != nil && (m.RecordKind == "" || m.RecordKind == RecordLegacy) &&
		m.Resource == nil && m.Representation == nil && m.Barriers == nil
}

// IsControl also protects unknown future versions of a resource record.
func (m *Metadata) IsControl() bool {
	return m != nil && (m.RecordKind == RecordResource || m.RecordKind == RecordBarriers || m.Resource != nil || m.Barriers != nil)
}

func (m *Metadata) IsResponse() bool {
	if m.IsLegacy() {
		return true
	}
	return m != nil && m.RecordKind == RecordRepresentation && m.Resource == nil && m.Barriers == nil &&
		m.Representation != nil && m.Representation.SchemaVersion == RepresentationSchemaVersion &&
		(m.Representation.Kind == RepresentationOrigin || m.Representation.Kind == RepresentationDerived)
}

// HasReadableChunks accepts either a complete representation or an immutable
// partial generation published after its response reached EOF. Filling objects
// without confirmed chunks, or with outstanding file ownership, are unreadable.
func (m *Metadata) HasReadableChunks() bool {
	if m == nil || m.RecordKind != RecordRepresentation || !m.IsResponse() || m.Size == 0 ||
		m.Size > math.MaxInt64 || m.BlockSize == 0 || m.BlockSize > math.MaxInt64 || m.Parts.Count() != 0 {
		return false
	}
	blocks := (m.Size-1)/m.BlockSize + 1
	last, ok := m.Chunks.Max()
	if !ok || uint64(last) >= blocks || blocks > uint64(math.MaxUint32)+1 {
		return false
	}
	switch m.Representation.State {
	case RepresentationComplete:
		return m.HasComplete()
	case RepresentationFilling:
		return uint64(m.Chunks.Count()) < blocks
	default:
		return false
	}
}

// HasReadableRange never treats a missing block as permission to splice bytes
// fetched later into this generation.
func (m *Metadata) HasReadableRange(start, end uint64) bool {
	if !m.HasReadableChunks() || start > end || end >= m.Size {
		return false
	}
	first, last := start/m.BlockSize, end/m.BlockSize
	if last-first+1 > uint64(m.Chunks.Count()) {
		return false
	}
	for index := first; index <= last; index++ {
		if !m.Chunks.Contains(uint32(index)) {
			return false
		}
	}
	return true
}
