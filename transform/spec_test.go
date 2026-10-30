package transform

import (
	"bytes"
	"errors"
	"net/http"
	"slices"
	"testing"

	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

func capabilities() transformv1.BackendCapabilities {
	mimes := []string{"image/jpeg", "image/png", "image/webp"}
	c := transformv1.BackendCapabilities{SchemaVersion: 1, Inputs: mimes, Outputs: []transformv1.Codec{{MIME: "image/jpeg", QualityMin: 1, QualityMax: 100}, {MIME: "image/png", Lossless: true, CompressionMax: 9}, {MIME: "image/webp", QualityMin: 1, QualityMax: 100, Lossless: true}}, PolicyVersion: "backend-p1", EncoderVersion: "encoder-1", FlattenAlpha: true}
	for _, input := range mimes {
		for _, output := range mimes {
			c.Conversions = append(c.Conversions, transformv1.Conversion{InputMIME: input, OutputMIME: output})
		}
	}
	return c
}
func source(mime string) transformv1.SourceInfo {
	return transformv1.SourceInfo{MIME: mime, Size: 128, Width: 10, Height: 10}
}
func accept(t *testing.T, value string) Accept {
	t.Helper()
	a, err := ParseAccept(http.Header{"Accept": {value}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func policy() transformv1.Policy {
	return transformv1.Policy{Formats: []string{"image/webp", "image/jpeg", "image/png"}, PolicyVersion: "route-p1"}
}
func pointer[T any](v T) *T { return &v }

func TestSpecNormalizesApplicableDefaultsAndOwnsIdentity(t *testing.T) {
	caps := capabilities()
	p := policy()
	opts := transformv1.RequestOptions{Format: "webp"}
	a := accept(t, "image/webp")
	d, err := Resolve(a, source("image/jpeg"), p, opts, caps)
	if err != nil || d.Spec == nil {
		t.Fatal(d, err)
	}
	spec := d.Spec.Spec()
	if spec.Quality == nil || *spec.Quality != 82 || spec.Lossless || spec.CompressionLevel != nil || spec.AlphaPolicy != "preserve" || spec.AnimationPolicy != "reject" || spec.OrientationPolicy != "preserve" || spec.ColorPolicy != "preserve" {
		t.Fatal(spec)
	}
	explicit, err := Resolve(a, source("image/jpeg"), p, transformv1.RequestOptions{Format: "webp", Quality: pointer(82)}, caps)
	if err != nil || explicit.Spec.Fingerprint() != d.Spec.Fingerprint() {
		t.Fatal("default expansion unstable", err)
	}
	data := d.Spec.JSON()
	data[0] = '!'
	*spec.Quality = 1
	if !bytes.Equal(explicit.Spec.JSON(), d.Spec.JSON()) || *d.Spec.Spec().Quality != 82 {
		t.Fatal("normalized spec was mutable")
	}
	reordered := capabilities()
	slices.Reverse(reordered.Inputs)
	slices.Reverse(reordered.Outputs)
	slices.Reverse(reordered.Conversions)
	same, err := Resolve(a, source("image/jpeg"), p, opts, reordered)
	if err != nil || same.Spec.Fingerprint() != d.Spec.Fingerprint() {
		t.Fatal("capability order changed identity", err)
	}
	for _, change := range []string{"route", "encoder", "capability", "backend-policy", "quality"} {
		t.Run(change, func(t *testing.T) {
			caps := capabilities()
			p := policy()
			switch change {
			case "route":
				p.PolicyVersion = "route-p2"
			case "encoder":
				caps.EncoderVersion = "encoder-2"
			case "capability":
				caps.Outputs[2].QualityMin = 2
			case "backend-policy":
				caps.PolicyVersion = "backend-p2"
			case "quality":
				p.Quality = 90
			}
			changed, err := Resolve(a, source("image/jpeg"), p, opts, caps)
			if err != nil || changed.Spec.Fingerprint() == d.Spec.Fingerprint() {
				t.Fatal("byte policy did not isolate derived ID", change, err)
			}
		})
	}
}
func TestPNGIsLosslessAndDoesNotReuseJPEGQuality(t *testing.T) {
	caps := capabilities()
	p := policy()
	a := accept(t, "image/png")
	first, err := Resolve(a, source("image/jpeg"), p, transformv1.RequestOptions{Format: "png"}, caps)
	if err != nil {
		t.Fatal(err)
	}
	p.Quality = 95
	second, err := Resolve(a, source("image/jpeg"), p, transformv1.RequestOptions{Format: "png"}, caps)
	if err != nil {
		t.Fatal(err)
	}
	spec := first.Spec.Spec()
	if !spec.Lossless || spec.Quality != nil || spec.CompressionLevel == nil || *spec.CompressionLevel != 6 || first.Spec.Fingerprint() != second.Spec.Fingerprint() {
		t.Fatal(spec)
	}
	if _, err = Resolve(a, source("image/jpeg"), p, transformv1.RequestOptions{Format: "png", Quality: pointer(80)}, caps); !errors.Is(err, ErrInvalidParameter) {
		t.Fatal("PNG accepted quality parameter", err)
	}
	zero, err := Resolve(a, source("image/jpeg"), p, transformv1.RequestOptions{Format: "png", CompressionLevel: pointer(0)}, caps)
	if err != nil || *zero.Spec.Spec().CompressionLevel != 0 || zero.Spec.Fingerprint() == first.Spec.Fingerprint() {
		t.Fatal("PNG zero level lost", err)
	}
}
func TestAlphaAnimationAndCodecConstraints(t *testing.T) {
	caps := capabilities()
	p := policy()
	png := source("image/png")
	png.HasAlpha = true
	if _, err := Resolve(accept(t, "image/jpeg"), png, p, transformv1.RequestOptions{Format: "jpeg"}, caps); !errors.Is(err, ErrAlphaLoss) {
		t.Fatal("alpha discarded", err)
	}
	p.AlphaPolicy, p.Background = "flatten", "#AbCdEF"
	flattened, err := Resolve(accept(t, "image/jpeg"), png, p, transformv1.RequestOptions{Format: "jpeg"}, caps)
	if err != nil || flattened.Spec.Spec().Background != "#abcdef" {
		t.Fatal(flattened, err)
	}
	caps.FlattenAlpha = false
	if _, err = Resolve(accept(t, "image/jpeg"), png, p, transformv1.RequestOptions{Format: "jpeg"}, caps); !errors.Is(err, ErrAlphaLoss) {
		t.Fatal("missing flatten capability ignored", err)
	}
	caps = capabilities()
	p = policy()
	png.Animated = true
	original, err := Resolve(accept(t, "image/webp,image/png;q=0.5"), png, p, transformv1.RequestOptions{}, caps)
	if err != nil || !original.PreserveSource || original.OutputMIME != "image/png" {
		t.Fatal("animation not preserved", original, err)
	}
	if _, err = Resolve(accept(t, "image/webp"), png, p, transformv1.RequestOptions{}, caps); !errors.Is(err, ErrAnimation) {
		t.Fatal("silently took first frame", err)
	}
	if _, err = Resolve(accept(t, "*/*"), png, p, transformv1.RequestOptions{Format: "webp"}, caps); !errors.Is(err, ErrAnimation) {
		t.Fatal("explicit target silently became source", err)
	}
	for _, options := range []transformv1.RequestOptions{{Format: "jpeg", Quality: pointer(0)}, {Format: "source", Quality: pointer(80)}, {Format: "jpeg", CompressionLevel: pointer(6)}, {Format: "jpeg", Lossless: pointer(true)}, {Format: "png", CompressionLevel: pointer(10)}, {Format: "webp", Lossless: pointer(true), Quality: pointer(80)}} {
		if _, err = Resolve(accept(t, "*/*"), source("image/jpeg"), policy(), options, caps); !errors.Is(err, ErrInvalidParameter) {
			t.Fatal(options, err)
		}
	}
	caps.Outputs[2].QualityMax = 90
	if _, err = Resolve(accept(t, "*/*"), source("image/jpeg"), policy(), transformv1.RequestOptions{Format: "webp", Quality: pointer(91)}, caps); !errors.Is(err, ErrInvalidParameter) {
		t.Fatal("quality exceeded worker contract", err)
	}
	lossless, err := Resolve(accept(t, "*/*"), source("image/jpeg"), policy(), transformv1.RequestOptions{Format: "webp", Lossless: pointer(true)}, caps)
	if err != nil || !lossless.Spec.Spec().Lossless || lossless.Spec.Spec().Quality != nil {
		t.Fatal(lossless, err)
	}
}
func TestCapabilitiesRequireExplicitVerifiedMatrix(t *testing.T) {
	caps := capabilities()
	caps.Conversions = []transformv1.Conversion{{InputMIME: "image/png", OutputMIME: "image/webp"}}
	if _, err := Resolve(accept(t, "image/webp"), source("image/jpeg"), policy(), transformv1.RequestOptions{Format: "webp"}, caps); !errors.Is(err, ErrNotAcceptable) {
		t.Fatal("assumed Cartesian backend support", err)
	}
	for _, mutate := range []func(*transformv1.BackendCapabilities){func(c *transformv1.BackendCapabilities) { c.EncoderVersion = "" }, func(c *transformv1.BackendCapabilities) { c.SchemaVersion = 2 }, func(c *transformv1.BackendCapabilities) { c.Outputs[0].QualityMax = 101 }, func(c *transformv1.BackendCapabilities) { c.Outputs[1].QualityMin = 1 }, func(c *transformv1.BackendCapabilities) { c.Inputs = append(c.Inputs, c.Inputs[0]) }, func(c *transformv1.BackendCapabilities) { c.Conversions = append(c.Conversions, c.Conversions[0]) }} {
		c := capabilities()
		mutate(&c)
		if err := ValidateCapabilities(c); !errors.Is(err, ErrInvalidCapabilities) {
			t.Fatal("invalid capability accepted", err)
		}
	}
}
