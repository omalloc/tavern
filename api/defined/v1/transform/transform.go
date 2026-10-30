// Package transform defines complete-image conversion contracts. Neither a
// partial cache descriptor nor an arbitrary remote URL is a conversion source.
package transform

import (
	"context"
	"io"
)

const SchemaVersion uint16 = 1

// SourceInfo is established by bounded inspection of complete source bytes.
// It is persisted with the source version so derived hits need no decoding.
type SourceInfo struct {
	MIME     string `json:"mime" yaml:"mime"`
	Size     int64  `json:"size" yaml:"size"`
	Width    int    `json:"width" yaml:"width"`
	Height   int    `json:"height" yaml:"height"`
	HasAlpha bool   `json:"has_alpha" yaml:"has_alpha"`
	Animated bool   `json:"animated" yaml:"animated"`
	// Version 1 conversion rejects orientation/color metadata until it has its own backend preservation gate.
	HasMetadata bool `json:"has_metadata" yaml:"has_metadata"`
}

type CompleteSource interface {
	Info() SourceInfo
	Open(context.Context) (io.ReadCloser, error)
}

// CompleteOutput owns temporary output storage. The caller must Close it on
// successful publication, failure and cancellation. Open returns a fresh reader.
type CompleteOutput interface {
	CompleteSource
	// Digest is the lowercase hexadecimal SHA-256 of final encoded bytes.
	Digest() string
	EncoderVersion() string
	io.Closer
}

type Codec struct {
	MIME           string `json:"mime" yaml:"mime"`
	QualityMin     int    `json:"quality_min,omitempty" yaml:"quality_min,omitempty"`
	QualityMax     int    `json:"quality_max,omitempty" yaml:"quality_max,omitempty"`
	CompressionMin int    `json:"compression_min,omitempty" yaml:"compression_min,omitempty"`
	CompressionMax int    `json:"compression_max,omitempty" yaml:"compression_max,omitempty"`
	Lossless       bool   `json:"lossless" yaml:"lossless"`
}
type Conversion struct {
	InputMIME  string `json:"input_mime" yaml:"input_mime"`
	OutputMIME string `json:"output_mime" yaml:"output_mime"`
}

type BackendCapabilities struct {
	SchemaVersion  uint16       `json:"schema_version" yaml:"schema_version"`
	Inputs         []string     `json:"input_formats" yaml:"input_formats"`
	Outputs        []Codec      `json:"output_formats" yaml:"output_formats"`
	Conversions    []Conversion `json:"conversions" yaml:"conversions"`
	FlattenAlpha   bool         `json:"flatten_alpha" yaml:"flatten_alpha"`
	PolicyVersion  string       `json:"policy_version" yaml:"policy_version"`
	EncoderVersion string       `json:"encoder_version" yaml:"encoder_version"`
}

// TransformSpec contains every operation affecting encoded bytes. Normalization
// expands applicable defaults and omits inapplicable codec options. Version 1
// has no resize/crop operations. Pointer scalars distinguish omitted from zero.
type TransformSpec struct {
	SchemaVersion     uint16 `json:"schema_version" yaml:"schema_version"`
	OutputMIME        string `json:"output_mime" yaml:"output_mime"`
	Quality           *int   `json:"quality,omitempty" yaml:"quality,omitempty"`
	Lossless          bool   `json:"lossless" yaml:"lossless"`
	CompressionLevel  *int   `json:"compression_level,omitempty" yaml:"compression_level,omitempty"`
	AlphaPolicy       string `json:"alpha_policy" yaml:"alpha_policy"`
	Background        string `json:"background,omitempty" yaml:"background,omitempty"`
	AnimationPolicy   string `json:"animation_policy" yaml:"animation_policy"`
	OrientationPolicy string `json:"orientation_policy" yaml:"orientation_policy"`
	ColorPolicy       string `json:"color_policy" yaml:"color_policy"`
}

type Transformer interface {
	Capabilities() BackendCapabilities
	Transform(context.Context, CompleteSource, TransformSpec) (CompleteOutput, error)
}

// Policy defaults are expanded during normalization. Quality applies only to
// lossy JPEG/WebP output; PNGCompression is a separate lossless setting.
type Policy struct {
	Formats           []string `json:"formats" yaml:"formats"`
	Quality           int      `json:"quality" yaml:"quality"`
	PNGCompression    *int     `json:"png_compression,omitempty" yaml:"png_compression,omitempty"`
	AlphaPolicy       string   `json:"alpha_policy" yaml:"alpha_policy"`
	Background        string   `json:"background,omitempty" yaml:"background,omitempty"`
	AnimationPolicy   string   `json:"animation_policy" yaml:"animation_policy"`
	OrientationPolicy string   `json:"orientation_policy" yaml:"orientation_policy"`
	ColorPolicy       string   `json:"color_policy" yaml:"color_policy"`
	PolicyVersion     string   `json:"policy_version" yaml:"policy_version"`
}
type RequestOptions struct {
	Format           string
	Quality          *int
	CompressionLevel *int
	Lossless         *bool
}
