package transform

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/andybalholm/brotli"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

type blobInspector struct {
	width, height int
	calls         int
}

func (i *blobInspector) Version() string { return "test-inspector" }
func (i *blobInspector) Inspect(_ context.Context, _ string, size, _ int64) (transformv1.SourceInfo, error) {
	i.calls++
	return transformv1.SourceInfo{MIME: "image/png", Size: size, Width: i.width, Height: i.height}, nil
}
func encodedBlob(t *testing.T, encoding string, raw []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var writer io.WriteCloser
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&buffer)
	case "br":
		writer = brotli.NewWriter(&buffer)
	default:
		return raw
	}
	writer.Write(raw)
	writer.Close()
	return buffer.Bytes()
}
func TestSpoolDecodedConfirmsEOFAndBoundsBothSizes(t *testing.T) {
	raw := bytes.Repeat([]byte("complete-image-bytes"), 50)
	for _, encoding := range []string{"identity", "gzip", "br"} {
		t.Run(encoding, func(t *testing.T) {
			dir := t.TempDir()
			inspector := &blobInspector{width: 8, height: 8}
			wire := encodedBlob(t, encoding, raw)
			blob, err := SpoolDecoded(t.Context(), bytes.NewReader(wire), encoding, dir, 4096, 100, inspector)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := blob.Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := io.ReadAll(reader)
			reader.Close()
			if err != nil || !bytes.Equal(decoded, raw) || blob.Info().Size != int64(len(raw)) {
				t.Fatal("not complete decoded bytes", err)
			}
			blob.Close()
			blob.Close()
			files, _ := filepath.Glob(filepath.Join(dir, "tavern-image-*"))
			if len(files) != 0 {
				t.Fatal("spool leaked", files)
			}
			if _, err = SpoolDecoded(t.Context(), bytes.NewReader(wire), encoding, dir, 100, 100, inspector); !errors.Is(err, ErrBudget) {
				t.Fatal("unbounded decoded input", err)
			}
			files, _ = filepath.Glob(filepath.Join(dir, "tavern-image-*"))
			if len(files) != 0 {
				t.Fatal("failed spool leaked", files)
			}
		})
	}
	for _, encoding := range []string{"gzip", "br"} {
		wire := encodedBlob(t, encoding, raw)
		wire = wire[:len(wire)-2]
		if blob, err := SpoolDecoded(t.Context(), bytes.NewReader(wire), encoding, t.TempDir(), 4096, 100, &blobInspector{width: 8, height: 8}); err == nil {
			blob.Close()
			t.Fatal("truncated encoding accepted", encoding)
		}
	}
}
func TestSpoolRejectsPixelBudgetUnknownEncodingAndCancellation(t *testing.T) {
	for _, test := range []struct {
		encoding  string
		inspector *blobInspector
		ctx       context.Context
		want      error
	}{
		{encoding: "identity", inspector: &blobInspector{width: 100, height: 100}, ctx: t.Context(), want: ErrBudget},
		{encoding: "gzip, br", inspector: &blobInspector{width: 1, height: 1}, ctx: t.Context(), want: ErrUnsupportedSource},
	} {
		dir := t.TempDir()
		if blob, err := SpoolDecoded(test.ctx, bytes.NewReader([]byte("image")), test.encoding, dir, 1000, 100, test.inspector); !errors.Is(err, test.want) {
			if blob != nil {
				blob.Close()
			}
			t.Fatal(err)
		}
		files, _ := os.ReadDir(dir)
		if len(files) != 0 {
			t.Fatal("failed inspection left temporary data")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if blob, err := SpoolDecoded(ctx, bytes.NewReader([]byte("image")), "identity", t.TempDir(), 1000, 100, &blobInspector{width: 1, height: 1}); !errors.Is(err, context.Canceled) {
		if blob != nil {
			blob.Close()
		}
		t.Fatal(err)
	}
}
