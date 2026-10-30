package transform

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func inspectBytes(t *testing.T, data []byte, pixels int64) error {
	t.Helper()
	blob, err := SpoolDecoded(context.Background(), bytes.NewReader(data), "identity", t.TempDir(), 1<<20, pixels, ImageInspector{})
	if blob != nil {
		blob.Close()
	}
	return err
}

func TestImageInspectorCompleteFormats(t *testing.T) {
	for _, alpha := range []bool{false, true} {
		fixtures, err := workerProbeImages(alpha)
		if err != nil {
			t.Fatal(err)
		}
		for mime, data := range fixtures {
			t.Run(mime+map[bool]string{true: "/alpha", false: "/opaque"}[alpha], func(t *testing.T) {
				dir := t.TempDir()
				blob, err := SpoolDecoded(context.Background(), bytes.NewReader(data), "identity", dir, 1<<20, 256, ImageInspector{})
				if err != nil {
					t.Fatal(err)
				}
				got := blob.Info()
				if got.MIME != mime || got.Width != 16 || got.Height != 16 || got.Size != int64(len(data)) || got.Animated || got.HasAlpha != (alpha && mime != "image/jpeg") {
					t.Fatalf("unexpected image info: %+v", got)
				}
				if err := blob.Close(); err != nil {
					t.Fatal(err)
				}
				entries, _ := os.ReadDir(dir)
				if len(entries) != 0 {
					t.Fatal("temporary image leaked")
				}
				for _, bad := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), 0)} {
					if err := inspectBytes(t, bad, 256); err == nil {
						t.Fatal("accepted incomplete container/trailing data")
					}
				}
				if err := inspectBytes(t, data, 255); !errors.Is(err, ErrBudget) {
					t.Fatalf("pixel budget: %v", err)
				}
			})
		}
	}
}

func TestImageInspectorInvalidFrames(t *testing.T) {
	fixtures, _ := workerProbeImages(false)
	pngData := fixtures["image/png"]
	chunks, err := pngChunks(pngData)
	if err != nil {
		t.Fatal(err)
	}
	var idat []byte
	for _, chunk := range chunks {
		if chunk.kind == "IDAT" {
			idat = append(idat, chunk.data...)
		}
	}
	makeAPNG := func(corrupt bool) []byte {
		var b bytes.Buffer
		b.WriteString("\x89PNG\r\n\x1a\n")
		appendPNGChunk(&b, "IHDR", chunks[0].data)
		ac := make([]byte, 8)
		binary.BigEndian.PutUint32(ac[:4], 2)
		appendPNGChunk(&b, "acTL", ac)
		frame := make([]byte, 26)
		binary.BigEndian.PutUint32(frame[4:8], 16)
		binary.BigEndian.PutUint32(frame[8:12], 16)
		appendPNGChunk(&b, "fcTL", frame)
		appendPNGChunk(&b, "IDAT", idat)
		binary.BigEndian.PutUint32(frame[:4], 1)
		appendPNGChunk(&b, "fcTL", frame)
		fd := make([]byte, 4)
		binary.BigEndian.PutUint32(fd, 2)
		fd = append(fd, idat...)
		if corrupt {
			fd[len(fd)-1] ^= 0xff
		}
		appendPNGChunk(&b, "fdAT", fd)
		appendPNGChunk(&b, "IEND", nil)
		return b.Bytes()
	}
	good := makeAPNG(false)
	blob, err := SpoolDecoded(context.Background(), bytes.NewReader(good), "identity", t.TempDir(), 1<<20, 768, ImageInspector{})
	if err != nil {
		t.Fatal(err)
	}
	if !blob.Info().Animated {
		t.Fatal("APNG reduced to first frame")
	}
	blob.Close()
	if err := inspectBytes(t, good, 767); !errors.Is(err, ErrBudget) {
		t.Fatalf("cumulative frame pixels: %v", err)
	}
	if err := inspectBytes(t, makeAPNG(true), 768); err == nil {
		t.Fatal("accepted corrupt APNG later frame")
	}

	static := fixtures["image/webp"]
	animate := func(corrupt bool) []byte {
		var b bytes.Buffer
		appendRIFFChunk(&b, "VP8X", []byte{2, 0, 0, 0, 15, 0, 0, 15, 0, 0})
		appendRIFFChunk(&b, "ANIM", []byte{255, 255, 255, 255, 0, 0})
		header := make([]byte, 16)
		header[6], header[9] = 15, 15
		frame := append(header, static[12:]...)
		appendRIFFChunk(&b, "ANMF", frame)
		if corrupt {
			frame = append([]byte{}, frame...)
			frame[16] = '?'
		}
		appendRIFFChunk(&b, "ANMF", frame)
		return frameWebP(b.Bytes(), 16, 16, false)
	}
	good = animate(false)
	blob, err = SpoolDecoded(context.Background(), bytes.NewReader(good), "identity", t.TempDir(), 1<<20, 512, ImageInspector{})
	if err != nil {
		t.Fatal(err)
	}
	if !blob.Info().Animated {
		t.Fatal("animated WebP reduced to first frame")
	}
	blob.Close()
	if err := inspectBytes(t, good, 511); !errors.Is(err, ErrBudget) {
		t.Fatalf("WebP cumulative pixels: %v", err)
	}
	if err := inspectBytes(t, animate(true), 512); err == nil {
		t.Fatal("accepted corrupt later WebP frame")
	}
}
func appendRIFFChunk(b *bytes.Buffer, kind string, data []byte) {
	b.WriteString(kind)
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(data)))
	b.Write(length[:])
	b.Write(data)
	if len(data)%2 != 0 {
		b.WriteByte(0)
	}
}
func TestImageInspectorFileAndCancellation(t *testing.T) {
	data, _ := workerProbeImages(false)
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, data["image/png"], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (ImageInspector{}).Inspect(context.Background(), path, 1, 256); !errors.Is(err, ErrBudget) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ImageInspector{}).Inspect(ctx, path, int64(len(data["image/png"])), 256); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	damaged := append([]byte{}, data["image/png"]...)
	damaged[len(damaged)-5] ^= 0xff
	if err := inspectBytes(t, damaged, 256); err == nil {
		t.Fatal("accepted bad PNG CRC")
	}
}
