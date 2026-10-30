package transform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

const ProtocolVersion = "1"
const SpecHeader = "X-Tavern-Transform-Spec"
const VersionHeader = "X-Tavern-Transform-Version"
const EncoderHeader = "X-Tavern-Encoder-Version"

var ErrWorker = errors.New("image worker contract failed")

type WorkerLimits struct {
	MaxInputBytes      int64         `json:"max_input_bytes" yaml:"max_input_bytes"`
	MaxOutputBytes     int64         `json:"max_output_bytes" yaml:"max_output_bytes"`
	MaxPixels          int64         `json:"max_pixels" yaml:"max_pixels"`
	Concurrency        int           `json:"concurrency" yaml:"concurrency"`
	QueueSize          int           `json:"queue_size" yaml:"queue_size"`
	Timeout            time.Duration `json:"timeout" yaml:"timeout"`
	TemporaryDirectory string        `json:"temporary_directory,omitempty" yaml:"temporary_directory,omitempty"`
}
type HTTPWorkerConfig struct {
	Endpoint     string                          `json:"endpoint" yaml:"endpoint"`
	Capabilities transformv1.BackendCapabilities `json:"capabilities" yaml:"capabilities"`
	Limits       WorkerLimits                    `json:"limits" yaml:"limits"`
}
type HTTPWorker struct {
	config    HTTPWorkerConfig
	client    *http.Client
	inspector Inspector
	budget    *WorkBudget
	verified  atomic.Bool
	verifyMu  sync.Mutex
}

var _ transformv1.Transformer = (*HTTPWorker)(nil)

func NewHTTPWorker(config HTTPWorkerConfig, client *http.Client) (*HTTPWorker, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.EscapedPath() != "/v1/transform" {
		return nil, ErrInvalidParameter
	}
	if err = ValidateCapabilities(config.Capabilities); err != nil {
		return nil, err
	}
	if !validVersion(config.Capabilities.EncoderVersion) || !validVersion(config.Capabilities.PolicyVersion) {
		return nil, ErrInvalidCapabilities
	}
	limits := config.Limits
	if limits.MaxInputBytes <= 0 || limits.MaxInputBytes > MaxEncodedImageBytes || limits.MaxOutputBytes <= 0 || limits.MaxOutputBytes > MaxEncodedImageBytes || limits.MaxPixels <= 0 || limits.Timeout <= 0 || limits.QueueSize <= 0 {
		return nil, ErrInvalidParameter
	}
	budget, err := NewObservedWorkBudget(limits.Concurrency, limits.QueueSize, "http", "worker")
	if err != nil {
		return nil, err
	}
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableCompression = true
		transport.MaxResponseHeaderBytes = 64 << 10
		client = &http.Client{Transport: transport}
	}
	owned := *client
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	encoded, _ := json.Marshal(config.Capabilities)
	config.Capabilities = transformv1.BackendCapabilities{}
	json.Unmarshal(encoded, &config.Capabilities)
	return &HTTPWorker{config: config, client: &owned, inspector: ImageInspector{}, budget: budget}, nil
}
func validVersion(version string) bool {
	if len(version) == 0 || len(version) > 128 {
		return false
	}
	for _, b := range []byte(version) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}
func (w *HTTPWorker) Capabilities() transformv1.BackendCapabilities {
	encoded, _ := json.Marshal(w.config.Capabilities)
	var result transformv1.BackendCapabilities
	json.Unmarshal(encoded, &result)
	return result
}
func (w *HTTPWorker) Verified() bool       { return w.verified.Load() }
func (w *HTTPWorker) Limits() WorkerLimits { return w.config.Limits }

func (w *HTTPWorker) Transform(parent context.Context, source transformv1.CompleteSource, spec transformv1.TransformSpec) (transformv1.CompleteOutput, error) {
	ctx, cancel := context.WithTimeout(parent, w.config.Limits.Timeout)
	defer cancel()
	release, err := w.budget.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()
	if source == nil {
		return nil, ErrUnsupportedSource
	}
	info := source.Info()
	if info.Size <= 0 || info.Size > w.config.Limits.MaxInputBytes || !pixelFit(info.Width, info.Height, w.config.Limits.MaxPixels) || info.Animated || info.HasMetadata {
		return nil, ErrUnsupportedSource
	}
	if err = validateWorkerSpec(info, spec, w.config.Capabilities); err != nil {
		return nil, err
	}
	reader, err := source.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	// Only complete local image bytes and generated contract fields enter RPC.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.config.Endpoint, reader)
	if err != nil {
		return nil, err
	}
	request.ContentLength = info.Size
	request.Header.Set("Content-Type", info.MIME)
	request.Header.Set("Accept", spec.OutputMIME)
	request.Header.Set("Accept-Encoding", "identity")
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	request.Header.Set(SpecHeader, base64.RawURLEncoding.EncodeToString(encoded))
	request.Header.Set(VersionHeader, ProtocolVersion)
	response, err := w.client.Do(request)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("worker status %d: %w", response.StatusCode, ErrWorker)
	}
	actual, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || actual != spec.OutputMIME || len(params) != 0 || len(response.Header.Values("Content-Type")) != 1 || len(response.Header.Values(EncoderHeader)) != 1 || response.Header.Get(EncoderHeader) != w.config.Capabilities.EncoderVersion {
		return nil, ErrWorker
	}
	encoding := strings.TrimSpace(strings.ToLower(response.Header.Get("Content-Encoding")))
	if len(response.Header.Values("Content-Encoding")) > 1 || encoding != "" && encoding != "identity" {
		return nil, ErrWorker
	}
	if response.ContentLength > w.config.Limits.MaxOutputBytes {
		return nil, ErrBudget
	}
	blob, err := SpoolDecoded(ctx, response.Body, "identity", w.config.Limits.TemporaryDirectory, w.config.Limits.MaxOutputBytes, w.config.Limits.MaxPixels, w.inspector)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			blob.Close()
		}
	}()
	output := blob.Info()
	if output.MIME != spec.OutputMIME || output.Width != info.Width || output.Height != info.Height || output.Animated || output.HasMetadata || response.ContentLength >= 0 && response.ContentLength != output.Size {
		return nil, ErrWorker
	}
	if spec.OutputMIME == "image/jpeg" && output.HasAlpha || source.Info().HasAlpha && spec.AlphaPolicy == "preserve" && !output.HasAlpha {
		return nil, ErrWorker
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	blob.SetEncoderVersion(w.config.Capabilities.EncoderVersion)
	complete, transferred = true, true
	// A successful output owns its concurrency permit until Close, so callers
	// cannot accumulate unbounded completed temporary files outside the budget.
	return &workerOutput{CompleteOutput: blob, release: release}, nil
}

type workerOutput struct {
	transformv1.CompleteOutput
	release func()
	once    sync.Once
	err     error
}

func (o *workerOutput) Close() error {
	o.once.Do(func() { o.err = o.CompleteOutput.Close(); o.release() })
	return o.err
}

func validateWorkerSpec(source transformv1.SourceInfo, spec transformv1.TransformSpec, caps transformv1.BackendCapabilities) error {
	if spec.SchemaVersion != transformv1.SchemaVersion || !supports(caps, source.MIME, spec.OutputMIME) || spec.AnimationPolicy != "reject" || spec.OrientationPolicy != "preserve" || spec.ColorPolicy != "preserve" {
		return ErrInvalidParameter
	}
	codec, ok := codecFor(caps, spec.OutputMIME)
	if !ok {
		return ErrInvalidCapabilities
	}
	switch spec.OutputMIME {
	case "image/png":
		if !spec.Lossless || spec.Quality != nil || spec.CompressionLevel == nil || *spec.CompressionLevel < codec.CompressionMin || *spec.CompressionLevel > codec.CompressionMax || spec.AlphaPolicy != "preserve" || spec.Background != "" {
			return ErrInvalidParameter
		}
	case "image/jpeg", "image/webp":
		if spec.CompressionLevel != nil {
			return ErrInvalidParameter
		}
		if spec.Lossless {
			if spec.OutputMIME != "image/webp" || !codec.Lossless || spec.Quality != nil {
				return ErrInvalidParameter
			}
		} else if spec.Quality == nil || *spec.Quality < codec.QualityMin || *spec.Quality > codec.QualityMax {
			return ErrInvalidParameter
		}
		if spec.OutputMIME == "image/webp" {
			if spec.AlphaPolicy != "preserve" || spec.Background != "" {
				return ErrInvalidParameter
			}
		} else {
			switch spec.AlphaPolicy {
			case "reject":
				if source.HasAlpha || spec.Background != "" {
					return ErrAlphaLoss
				}
			case "flatten":
				if !caps.FlattenAlpha || len(spec.Background) != 7 || spec.Background[0] != '#' {
					return ErrAlphaLoss
				}
				for _, c := range spec.Background[1:] {
					if !strings.ContainsRune("0123456789abcdef", c) {
						return ErrInvalidParameter
					}
				}
			default:
				return ErrInvalidParameter
			}
		}
	default:
		return ErrInvalidParameter
	}
	return nil
}

var _ io.Closer = (*workerOutput)(nil)

func (w *HTTPWorker) Close() error { w.client.CloseIdleConnections(); return nil }

var _ io.Closer = (*HTTPWorker)(nil)
