package transform

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

var (
	ErrInvalidCapabilities = errors.New("invalid transformer capabilities")
	ErrUnsupportedSource   = errors.New("unsupported complete source image")
	ErrAlphaLoss           = errors.New("alpha cannot be discarded without an explicit flatten background")
	ErrAnimation           = errors.New("animated image conversion is unsupported")
)

// NormalizedSpec is immutable; all accessors return owned copies. The exact
// canonical JSON sent to a worker is also used for its storage identity.
type NormalizedSpec struct {
	spec          transformv1.TransformSpec
	canonical     []byte
	fingerprint   string
	policyVersion string
}

func (s NormalizedSpec) Spec() transformv1.TransformSpec { return cloneSpec(s.spec) }
func (s NormalizedSpec) JSON() []byte                    { return slices.Clone(s.canonical) }
func (s NormalizedSpec) Fingerprint() string             { return s.fingerprint }

func (s NormalizedSpec) PolicyVersion() string { return s.policyVersion }

type Decision struct {
	OutputMIME     string
	PreserveSource bool
	Spec           *NormalizedSpec
}

func cloneSpec(s transformv1.TransformSpec) transformv1.TransformSpec {
	if s.Quality != nil {
		v := *s.Quality
		s.Quality = &v
	}
	if s.CompressionLevel != nil {
		v := *s.CompressionLevel
		s.CompressionLevel = &v
	}
	return s
}

func ValidateCapabilities(c transformv1.BackendCapabilities) error {
	if c.SchemaVersion != transformv1.SchemaVersion || c.PolicyVersion == "" || c.EncoderVersion == "" || len(c.Inputs) == 0 || len(c.Outputs) == 0 || len(c.Conversions) == 0 {
		return ErrInvalidCapabilities
	}
	inputs := make(map[string]bool)
	for _, input := range c.Inputs {
		if !knownMIME(input) || inputs[input] {
			return ErrInvalidCapabilities
		}
		inputs[input] = true
	}
	outputs := make(map[string]bool)
	for _, codec := range c.Outputs {
		if !knownMIME(codec.MIME) || outputs[codec.MIME] {
			return ErrInvalidCapabilities
		}
		outputs[codec.MIME] = true
		switch codec.MIME {
		case "image/png":
			if codec.QualityMin != 0 || codec.QualityMax != 0 || !codec.Lossless || codec.CompressionMin < 0 || codec.CompressionMax > 9 || codec.CompressionMin > codec.CompressionMax {
				return ErrInvalidCapabilities
			}
		default:
			if codec.QualityMin < 1 || codec.QualityMax > 100 || codec.QualityMin > codec.QualityMax || codec.CompressionMin != 0 || codec.CompressionMax != 0 || codec.MIME == "image/jpeg" && codec.Lossless {
				return ErrInvalidCapabilities
			}
		}
	}
	seen := make(map[transformv1.Conversion]bool)
	for _, pair := range c.Conversions {
		if !inputs[pair.InputMIME] || !outputs[pair.OutputMIME] || seen[pair] {
			return ErrInvalidCapabilities
		}
		seen[pair] = true
	}
	return nil
}
func capabilitiesFingerprint(c transformv1.BackendCapabilities) string {
	c.Inputs = slices.Clone(c.Inputs)
	sort.Strings(c.Inputs)
	c.Outputs = slices.Clone(c.Outputs)
	sort.Slice(c.Outputs, func(i, j int) bool { return c.Outputs[i].MIME < c.Outputs[j].MIME })
	c.Conversions = slices.Clone(c.Conversions)
	sort.Slice(c.Conversions, func(i, j int) bool {
		a, b := c.Conversions[i], c.Conversions[j]
		if a.InputMIME == b.InputMIME {
			return a.OutputMIME < b.OutputMIME
		}
		return a.InputMIME < b.InputMIME
	})
	data, _ := json.Marshal(c)
	return object.TransformFingerprint(data, "capabilities", "v1")
}
func supports(c transformv1.BackendCapabilities, input, output string) bool {
	for _, pair := range c.Conversions {
		if pair.InputMIME == input && pair.OutputMIME == output {
			return true
		}
	}
	return false
}
func codecFor(c transformv1.BackendCapabilities, output string) (transformv1.Codec, bool) {
	for _, codec := range c.Outputs {
		if codec.MIME == output {
			return codec, true
		}
	}
	return transformv1.Codec{}, false
}

// Resolve uses source metadata only. It never reads/decodes a source or calls a
// worker, so a provider can locate a finished product without opening its body.
func Resolve(a Accept, source transformv1.SourceInfo, policy transformv1.Policy, options transformv1.RequestOptions, caps transformv1.BackendCapabilities) (Decision, error) {
	if err := ValidateCapabilities(caps); err != nil {
		return Decision{}, err
	}
	if !knownMIME(source.MIME) || source.Size <= 0 || source.Width <= 0 || source.Height <= 0 || !slices.Contains(caps.Inputs, source.MIME) {
		return Decision{}, ErrUnsupportedSource
	}
	policy, err := normalizePolicy(policy, caps)
	if err != nil {
		return Decision{}, err
	}
	available := make([]string, 0, len(policy.Formats))
	for _, mime := range policy.Formats {
		if mime == source.MIME || supports(caps, source.MIME, mime) {
			available = append(available, mime)
		}
	}
	output, err := a.Choose(source.MIME, available, options.Format)
	if err != nil {
		return Decision{}, err
	}
	// Explicitly inapplicable fields are errors, even on an otherwise identity
	// response. Configured defaults are instead removed from irrelevant codecs.
	if output == "image/png" && options.Quality != nil || output != "image/png" && options.CompressionLevel != nil || output == "image/jpeg" && options.Lossless != nil && *options.Lossless || output == "image/png" && options.Lossless != nil && !*options.Lossless {
		return Decision{}, ErrInvalidParameter
	}
	explicitOptions := options.Quality != nil || options.CompressionLevel != nil || options.Lossless != nil
	if strings.EqualFold(options.Format, "source") && explicitOptions {
		return Decision{}, ErrInvalidParameter
	}
	if output == source.MIME && !explicitOptions {
		return Decision{OutputMIME: source.MIME, PreserveSource: true}, nil
	}
	if source.Animated {
		if policy.AnimationPolicy == "preserve_source" && options.Format == "" && !explicitOptions && a.Quality(source.MIME) > 0 {
			return Decision{OutputMIME: source.MIME, PreserveSource: true}, nil
		}
		return Decision{}, ErrAnimation
	}
	if !supports(caps, source.MIME, output) {
		return Decision{}, ErrNotAcceptable
	}
	codec, ok := codecFor(caps, output)
	if !ok {
		return Decision{}, ErrInvalidCapabilities
	}
	spec := transformv1.TransformSpec{SchemaVersion: transformv1.SchemaVersion, OutputMIME: output, AlphaPolicy: "preserve", AnimationPolicy: "reject", OrientationPolicy: policy.OrientationPolicy, ColorPolicy: policy.ColorPolicy}
	switch output {
	case "image/png":
		spec.Lossless = true
		level := *policy.PNGCompression
		if options.CompressionLevel != nil {
			level = *options.CompressionLevel
		}
		if level < codec.CompressionMin || level > codec.CompressionMax {
			return Decision{}, ErrInvalidParameter
		}
		spec.CompressionLevel = &level
	case "image/jpeg", "image/webp":
		if output == "image/webp" && options.Lossless != nil {
			spec.Lossless = *options.Lossless
		}
		if spec.Lossless {
			if !codec.Lossless || options.Quality != nil {
				return Decision{}, ErrInvalidParameter
			}
		} else {
			quality := policy.Quality
			if options.Quality != nil {
				quality = *options.Quality
			}
			if quality < codec.QualityMin || quality > codec.QualityMax {
				return Decision{}, ErrInvalidParameter
			}
			spec.Quality = &quality
		}
		if output == "image/jpeg" {
			spec.AlphaPolicy = policy.AlphaPolicy
			if policy.AlphaPolicy == "flatten" {
				if !caps.FlattenAlpha {
					return Decision{}, ErrAlphaLoss
				}
				spec.Background = policy.Background
			} else if source.HasAlpha {
				return Decision{}, ErrAlphaLoss
			}
		}
	}
	data, _ := json.Marshal(spec)
	versions, _ := json.Marshal([]string{policy.PolicyVersion, capabilitiesFingerprint(caps)})
	normalized := &NormalizedSpec{spec: cloneSpec(spec), canonical: data, fingerprint: object.TransformFingerprint(data, string(versions), caps.EncoderVersion), policyVersion: string(versions)}
	return Decision{OutputMIME: output, Spec: normalized}, nil
}

func normalizePolicy(p transformv1.Policy, caps transformv1.BackendCapabilities) (transformv1.Policy, error) {
	if p.PolicyVersion == "" {
		return p, fmt.Errorf("policy_version must be explicit: %w", ErrInvalidParameter)
	}
	p.Formats = slices.Clone(p.Formats)
	if len(p.Formats) == 0 {
		for _, mime := range []string{"image/webp", "image/jpeg", "image/png"} {
			if _, ok := codecFor(caps, mime); ok {
				p.Formats = append(p.Formats, mime)
			}
		}
	}
	seen := make(map[string]bool)
	for _, mime := range p.Formats {
		if _, ok := codecFor(caps, mime); !ok || seen[mime] {
			return p, ErrInvalidParameter
		}
		seen[mime] = true
	}
	if p.Quality == 0 {
		p.Quality = 82
	}
	if p.Quality < 1 || p.Quality > 100 {
		return p, ErrInvalidParameter
	}
	if p.PNGCompression == nil {
		v := 6
		p.PNGCompression = &v
	} else {
		v := *p.PNGCompression
		p.PNGCompression = &v
	}
	if *p.PNGCompression < 0 || *p.PNGCompression > 9 {
		return p, ErrInvalidParameter
	}
	if p.AlphaPolicy == "" {
		p.AlphaPolicy = "reject"
	}
	if p.AlphaPolicy != "reject" && p.AlphaPolicy != "flatten" {
		return p, ErrInvalidParameter
	}
	if p.AlphaPolicy == "flatten" {
		if len(p.Background) != 7 || p.Background[0] != '#' {
			return p, ErrInvalidParameter
		}
		if _, err := hex.DecodeString(p.Background[1:]); err != nil {
			return p, ErrInvalidParameter
		}
		p.Background = strings.ToLower(p.Background)
	} else if p.Background != "" {
		return p, ErrInvalidParameter
	}
	if p.AnimationPolicy == "" {
		p.AnimationPolicy = "preserve_source"
	}
	if p.AnimationPolicy != "preserve_source" && p.AnimationPolicy != "reject" {
		return p, ErrInvalidParameter
	}
	if p.OrientationPolicy == "" {
		p.OrientationPolicy = "preserve"
	}
	if p.ColorPolicy == "" {
		p.ColorPolicy = "preserve"
	}
	if p.OrientationPolicy != "preserve" || p.ColorPolicy != "preserve" {
		return p, ErrInvalidParameter
	}
	return p, nil
}
