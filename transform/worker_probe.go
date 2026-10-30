package transform

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

// These tiny, self-generated fixtures were encoded from the same Go-created
// pixels with cwebp 1.6.0 lossless. The proxy itself never invokes an encoder.
const opaqueProbeWebP = "UklGRiwAAABXRUJQVlA4TCAAAAAvD8ADALmM6H/sClGIgvc/ICQgTPD/sKgORMQEgNW7AA=="
const alphaProbeWebP = "UklGRh4AAABXRUJQVlA4TBEAAAAvD8ADEAdQwAIVuICBiOh/AAA="

type ContractCase struct {
	InputMIME  string `json:"input_mime"`
	OutputMIME string `json:"output_mime"`
	Variant    string `json:"variant"`
	Digest     string `json:"digest"`
}
type ContractReport struct {
	EncoderVersion        string         `json:"encoder_version"`
	CapabilityFingerprint string         `json:"capability_fingerprint"`
	Cases                 []ContractCase `json:"cases"`
}

// Verify exercises every declared pair against the configured real endpoint.
// New workers are unverified; public route binding must require this gate. It
// never trusts a capability declaration alone or a successful response header.
func (w *HTTPWorker) Verify(ctx context.Context) (*ContractReport, error) {
	w.verifyMu.Lock()
	defer w.verifyMu.Unlock()
	w.verified.Store(false)
	report := &ContractReport{EncoderVersion: w.config.Capabilities.EncoderVersion, CapabilityFingerprint: capabilitiesFingerprint(w.config.Capabilities)}
	fixtures, err := workerProbeImages(false)
	if err != nil {
		return nil, err
	}
	alphaFixtures, err := workerProbeImages(true)
	if err != nil {
		return nil, err
	}
	for _, pair := range w.config.Capabilities.Conversions {
		input, err := SpoolDecoded(ctx, bytes.NewReader(fixtures[pair.InputMIME]), "identity", w.config.Limits.TemporaryDirectory, w.config.Limits.MaxInputBytes, w.config.Limits.MaxPixels, w.inspector)
		if err != nil {
			return nil, fmt.Errorf("probe source %s: %w", pair.InputMIME, err)
		}
		codec, _ := codecFor(w.config.Capabilities, pair.OutputMIME)
		variants := []struct {
			name                 string
			quality, compression *int
			lossless             *bool
		}{}
		if pair.OutputMIME == "image/png" {
			level := max(codec.CompressionMin, min(6, codec.CompressionMax))
			variants = append(variants, struct {
				name                 string
				quality, compression *int
				lossless             *bool
			}{name: "lossless-png", compression: &level})
		} else {
			low, high := codec.QualityMin, codec.QualityMax
			variants = append(variants, struct {
				name                 string
				quality, compression *int
				lossless             *bool
			}{name: "quality-min", quality: &low})
			if high != low {
				variants = append(variants, struct {
					name                 string
					quality, compression *int
					lossless             *bool
				}{name: "quality-max", quality: &high})
			}
			if pair.OutputMIME == "image/webp" && codec.Lossless {
				lossless := true
				variants = append(variants, struct {
					name                 string
					quality, compression *int
					lossless             *bool
				}{name: "lossless-webp", lossless: &lossless})
			}
		}
		qualityDigest := ""
		for _, variant := range variants {
			opts := transformv1.RequestOptions{Format: pair.OutputMIME, Quality: variant.quality, CompressionLevel: variant.compression, Lossless: variant.lossless}
			decision, err := Resolve(Accept{missing: true}, input.Info(), transformv1.Policy{Formats: []string{pair.OutputMIME}, PolicyVersion: "worker-contract-v1"}, opts, w.config.Capabilities)
			if err != nil {
				input.Close()
				return nil, err
			}
			output, err := w.Transform(ctx, input, decision.Spec.Spec())
			if err != nil {
				input.Close()
				return nil, fmt.Errorf("probe %s to %s/%s: %w", pair.InputMIME, pair.OutputMIME, variant.name, err)
			}
			digest := output.Digest()
			if variant.name == "quality-min" {
				qualityDigest = digest
			} else if variant.name == "quality-max" && codec.QualityMax-codec.QualityMin >= 20 && qualityDigest == digest {
				output.Close()
				input.Close()
				return nil, fmt.Errorf("worker ignored quality bounds: %w", ErrWorker)
			}
			if decision.Spec.Spec().Lossless {
				if err = equalImagePixels(ctx, input, output); err != nil {
					output.Close()
					input.Close()
					return nil, err
				}
			}
			report.Cases = append(report.Cases, ContractCase{pair.InputMIME, pair.OutputMIME, variant.name, digest})
			output.Close()
		}
		input.Close()
		if pair.InputMIME == "image/png" || pair.InputMIME == "image/webp" {
			alpha, err := SpoolDecoded(ctx, bytes.NewReader(alphaFixtures[pair.InputMIME]), "identity", w.config.Limits.TemporaryDirectory, w.config.Limits.MaxInputBytes, w.config.Limits.MaxPixels, w.inspector)
			if err != nil {
				return nil, err
			}
			if pair.OutputMIME != "image/jpeg" || w.config.Capabilities.FlattenAlpha {
				policy := transformv1.Policy{Formats: []string{pair.OutputMIME}, PolicyVersion: "worker-contract-v1"}
				quality := codec.QualityMax
				opts := transformv1.RequestOptions{Format: pair.OutputMIME, Quality: &quality}
				if pair.OutputMIME == "image/png" {
					opts.Quality = nil
					level := max(codec.CompressionMin, min(6, codec.CompressionMax))
					opts.CompressionLevel = &level
				} else if pair.OutputMIME == "image/jpeg" {
					policy.AlphaPolicy, policy.Background = "flatten", "#ffffff"
				}
				decision, err := Resolve(Accept{missing: true}, alpha.Info(), policy, opts, w.config.Capabilities)
				if err != nil {
					alpha.Close()
					return nil, err
				}
				output, err := w.Transform(ctx, alpha, decision.Spec.Spec())
				if err != nil {
					alpha.Close()
					return nil, err
				}
				variant := "alpha-preserve"
				if pair.OutputMIME == "image/jpeg" {
					variant = "alpha-flatten"
					err = verifyWhiteFlatten(ctx, output)
				} else if !output.Info().HasAlpha {
					err = ErrAlphaLoss
				}
				if err != nil {
					output.Close()
					alpha.Close()
					return nil, err
				}
				report.Cases = append(report.Cases, ContractCase{pair.InputMIME, pair.OutputMIME, variant, output.Digest()})
				output.Close()
			} else {
				quality := codec.QualityMax
				spec := transformv1.TransformSpec{SchemaVersion: 1, OutputMIME: "image/jpeg", Quality: &quality, AlphaPolicy: "reject", AnimationPolicy: "reject", OrientationPolicy: "preserve", ColorPolicy: "preserve"}
				output, err := w.Transform(ctx, alpha, spec)
				if output != nil {
					output.Close()
				}
				if !errors.Is(err, ErrAlphaLoss) {
					alpha.Close()
					return nil, fmt.Errorf("transparent JPEG rejection: %w", ErrWorker)
				}
			}
			alpha.Close()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.verified.Store(true)
	return report, nil
}

func workerProbeImages(alpha bool) (map[string][]byte, error) {
	canvas := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			pixel := color.NRGBA{uint8(x * 16), uint8(y * 16), uint8((x + y) * 8), 255}
			if alpha {
				pixel = color.NRGBA{64, 128, 192, 128}
			}
			canvas.SetNRGBA(x, y, pixel)
		}
	}
	var pngBytes, jpegBytes bytes.Buffer
	if err := png.Encode(&pngBytes, canvas); err != nil {
		return nil, err
	}
	if err := jpeg.Encode(&jpegBytes, canvas, &jpeg.Options{Quality: 95}); err != nil {
		return nil, err
	}
	encoded := opaqueProbeWebP
	if alpha {
		encoded = alphaProbeWebP
	}
	webpBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"image/png": pngBytes.Bytes(), "image/jpeg": jpegBytes.Bytes(), "image/webp": webpBytes}, nil
}
func decodeComplete(ctx context.Context, source transformv1.CompleteSource) (image.Image, error) {
	reader, err := source.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	img, _, err := image.Decode(&contextReader{ctx, reader})
	return img, err
}
func equalImagePixels(ctx context.Context, source, output transformv1.CompleteSource) error {
	original, err := decodeComplete(ctx, source)
	if err != nil {
		return err
	}
	actual, err := decodeComplete(ctx, output)
	if err != nil {
		return err
	}
	if original.Bounds() != actual.Bounds() {
		return ErrWorker
	}
	for y := original.Bounds().Min.Y; y < original.Bounds().Max.Y; y++ {
		for x := original.Bounds().Min.X; x < original.Bounds().Max.X; x++ {
			a := color.NRGBAModel.Convert(original.At(x, y)).(color.NRGBA)
			b := color.NRGBAModel.Convert(actual.At(x, y)).(color.NRGBA)
			if a != b {
				return fmt.Errorf("worker changed lossless pixels: %w", ErrWorker)
			}
		}
	}
	return nil
}
func verifyWhiteFlatten(ctx context.Context, output transformv1.CompleteSource) error {
	img, err := decodeComplete(ctx, output)
	if err != nil {
		return err
	}
	pixel := color.NRGBAModel.Convert(img.At(8, 8)).(color.NRGBA)
	expected := []int{159, 191, 223}
	actual := []int{int(pixel.R), int(pixel.G), int(pixel.B)}
	for i, value := range actual {
		if value < expected[i]-20 || value > expected[i]+20 {
			return fmt.Errorf("worker ignored flatten background: %w", ErrWorker)
		}
	}
	return nil
}
