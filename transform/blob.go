package transform

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

	"github.com/andybalholm/brotli"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

var ErrBudget = errors.New("image byte or pixel budget exceeded")

// Inspector must validate complete image bytes, not only a header. It is
// injected independently of the encoder so a finished hit does not call either.
type Inspector interface {
	Version() string
	Inspect(context.Context, string, int64, int64) (transformv1.SourceInfo, error)
}

// FileBlob owns a complete temporary object. All paths originate from local
// spooling, never a client URL. Close is idempotent and removes owned bytes.
type FileBlob struct {
	path            string
	info            transformv1.SourceInfo
	digest, encoder string
	once            sync.Once
	closeErr        error
}

var _ transformv1.CompleteOutput = (*FileBlob)(nil)

func (b *FileBlob) Info() transformv1.SourceInfo { return b.info }
func (b *FileBlob) Digest() string               { return b.digest }
func (b *FileBlob) EncoderVersion() string       { return b.encoder }
func (b *FileBlob) Open(ctx context.Context) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.Open(b.path)
}
func (b *FileBlob) Close() error {
	b.once.Do(func() {
		b.closeErr = os.Remove(b.path)
		if errors.Is(b.closeErr, os.ErrNotExist) {
			b.closeErr = nil
		}
	})
	return b.closeErr
}
func (b *FileBlob) SetEncoderVersion(version string) { b.encoder = version }

// SpoolDecoded bounds both wire and decoded sizes and confirms decoder EOF,
// including gzip checksums. The caller retains its HTTP content-encoding metadata.
func SpoolDecoded(ctx context.Context, body io.Reader, encoding, directory string, maxBytes, maxPixels int64, inspector Inspector) (*FileBlob, error) {
	if maxBytes <= 0 || maxBytes == math.MaxInt64 || maxPixels <= 0 || inspector == nil {
		return nil, ErrInvalidParameter
	}
	wire := &limitedInput{reader: body, remaining: maxBytes}
	var decoded io.Reader = wire
	var closer io.Closer
	switch encoding {
	case "", "identity":
	case "gzip":
		reader, err := gzip.NewReader(wire)
		if err != nil {
			return nil, err
		}
		decoded, closer = reader, reader
	case "br":
		decoded = brotli.NewReader(wire)
	default:
		return nil, fmt.Errorf("unsupported image content encoding %q: %w", encoding, ErrUnsupportedSource)
	}
	if closer != nil {
		defer closer.Close()
	}
	file, err := os.CreateTemp(directory, "tavern-image-*")
	if err != nil {
		return nil, err
	}
	blob := &FileBlob{path: file.Name()}
	success := false
	defer func() {
		file.Close()
		if !success {
			blob.Close()
		}
	}()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(&contextReader{ctx, decoded}, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if n > maxBytes {
		return nil, ErrBudget
	}
	if n == 0 {
		return nil, ErrUnsupportedSource
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	info, err := inspector.Inspect(ctx, blob.path, n, maxPixels)
	if err != nil {
		return nil, err
	}
	if info.Size != n || info.Width <= 0 || info.Height <= 0 || int64(info.Width) > maxPixels/int64(info.Height) {
		return nil, ErrBudget
	}
	blob.info, blob.digest = info, hex.EncodeToString(digest.Sum(nil))
	success = true
	return blob, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type limitedInput struct {
	reader    io.Reader
	remaining int64
}

func (r *limitedInput) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return 0, ErrBudget
	}
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return n, ErrBudget
	}
	return n, err
}
