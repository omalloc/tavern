package transform

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"

	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	_ "golang.org/x/image/webp"
)

const MaxEncodedImageBytes int64 = 128 << 20
const maxImageChunks = 8192
const maxImageFrames = 128

type ImageInspector struct{}

var _ Inspector = ImageInspector{}

func (ImageInspector) Version() string { return "complete-image-v1-ximage-0.47" }

// Inspect checks the complete container, then every decoded frame within the
// cumulative pixel budget. Animated files are described, never silently reduced
// to a single frame for conversion. Source policy decides preservation/rejection.
func (ImageInspector) Inspect(ctx context.Context, path string, size, maxPixels int64) (transformv1.SourceInfo, error) {
	if err := ctx.Err(); err != nil {
		return transformv1.SourceInfo{}, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return transformv1.SourceInfo{}, err
	}
	if size <= 0 || size > MaxEncodedImageBytes || stat.Size() != size || maxPixels <= 0 {
		return transformv1.SourceInfo{}, ErrBudget
	}
	file, err := os.Open(path)
	if err != nil {
		return transformv1.SourceInfo{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx, file}, size+1))
	if err != nil {
		return transformv1.SourceInfo{}, err
	}
	if int64(len(data)) != size {
		return transformv1.SourceInfo{}, ErrBudget
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return inspectWebP(ctx, data, maxPixels)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return transformv1.SourceInfo{}, ErrUnsupportedSource
	}
	if format != "png" && format != "jpeg" {
		return transformv1.SourceInfo{}, ErrUnsupportedSource
	}
	if !pixelFit(config.Width, config.Height, maxPixels) {
		return transformv1.SourceInfo{}, ErrBudget
	}
	var chunks []imageChunk
	if format == "png" {
		chunks, err = pngChunks(data)
	} else {
		err = completeJPEG(data)
	}
	if err != nil {
		return transformv1.SourceInfo{}, err
	}
	decoded, _, err := image.Decode(&contextReader{ctx, bytes.NewReader(data)})
	if err != nil {
		return transformv1.SourceInfo{}, ErrUnsupportedSource
	}
	info := transformv1.SourceInfo{MIME: "image/" + format, Size: int64(len(data)), Width: config.Width, Height: config.Height, HasAlpha: hasAlpha(decoded)}
	if format == "png" {
		animated, alpha, err := inspectAPNG(ctx, chunks, config.Width, config.Height, maxPixels)
		if err != nil {
			return transformv1.SourceInfo{}, err
		}
		info.Animated, info.HasAlpha = animated, info.HasAlpha || alpha
	}
	return info, ctx.Err()
}
func pixelFit(width, height int, budget int64) bool {
	return width > 0 && height > 0 && int64(width) <= budget/int64(height)
}
func hasAlpha(img image.Image) bool {
	if opaque, ok := img.(interface{ Opaque() bool }); ok {
		return !opaque.Opaque()
	}
	return true
}

type imageChunk struct {
	kind string
	data []byte
}

func pngChunks(data []byte) ([]imageChunk, error) {
	if len(data) < 8 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return nil, ErrUnsupportedSource
	}
	var chunks []imageChunk
	for offset := 8; offset < len(data); {
		if len(chunks) >= maxImageChunks || len(data)-offset < 12 {
			return nil, ErrUnsupportedSource
		}
		n := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		if n > uint64(len(data)-offset-12) {
			return nil, ErrUnsupportedSource
		}
		end := offset + 8 + int(n)
		kind := string(data[offset+4 : offset+8])
		if crc32.ChecksumIEEE(data[offset+4:end]) != binary.BigEndian.Uint32(data[end:end+4]) {
			return nil, ErrUnsupportedSource
		}
		if len(chunks) == 0 && (kind != "IHDR" || n != 13) || len(chunks) > 0 && kind == "IHDR" {
			return nil, ErrUnsupportedSource
		}
		chunks = append(chunks, imageChunk{kind, data[offset+8 : end]})
		offset = end + 4
		if kind == "IEND" {
			if n != 0 || offset != len(data) {
				return nil, ErrUnsupportedSource
			}
			return chunks, nil
		}
	}
	return nil, ErrUnsupportedSource
}
func appendPNGChunk(dst *bytes.Buffer, kind string, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	dst.Write(length[:])
	dst.WriteString(kind)
	dst.Write(data)
	checksum := crc32.NewIEEE()
	checksum.Write([]byte(kind))
	checksum.Write(data)
	binary.BigEndian.PutUint32(length[:], checksum.Sum32())
	dst.Write(length[:])
}

func completeJPEG(data []byte) error {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return ErrUnsupportedSource
	}
	offset := 2
	for steps := 0; offset < len(data) && steps < maxImageChunks; steps++ {
		if data[offset] != 0xff {
			return ErrUnsupportedSource
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return ErrUnsupportedSource
		}
		marker := data[offset]
		offset++
		if marker == 0xd9 {
			if offset == len(data) {
				return nil
			}
			return ErrUnsupportedSource
		}
		if marker == 0x00 || marker == 0xd8 || marker >= 0xd0 && marker <= 0xd7 {
			return ErrUnsupportedSource
		}
		if marker == 0x01 {
			continue
		}
		if len(data)-offset < 2 {
			return ErrUnsupportedSource
		}
		length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if length < 2 || length > len(data)-offset {
			return ErrUnsupportedSource
		}
		offset += length
		if marker == 0xda {
			for offset < len(data) {
				next := bytes.IndexByte(data[offset:], 0xff)
				if next < 0 {
					return ErrUnsupportedSource
				}
				start := offset + next
				offset = start + 1
				for offset < len(data) && data[offset] == 0xff {
					offset++
				}
				if offset >= len(data) {
					return ErrUnsupportedSource
				}
				m := data[offset]
				if m == 0 || m >= 0xd0 && m <= 0xd7 {
					offset++
					continue
				}
				offset = start
				break
			}
		}
	}
	return ErrUnsupportedSource
}

// inspectAPNG reconstructs each fdAT frame as a bounded standalone PNG and
// decodes it. A valid first IDAT cannot hide a corrupt or enormous later frame.
func inspectAPNG(ctx context.Context, chunks []imageChunk, canvasW, canvasH int, budget int64) (bool, bool, error) {
	var declaration uint32
	var seenAnimation bool
	var common []imageChunk
	var current *pngFrame
	frames := 0
	sequence := uint32(0)
	pixels := int64(canvasW) * int64(canvasH)
	alpha := false
	seenIDAT := false
	finish := func() error {
		if current == nil {
			return nil
		}
		if len(current.data) == 0 {
			return ErrUnsupportedSource
		}
		if !pixelFit(current.width, current.height, budget-pixels) {
			return ErrBudget
		}
		pixels += int64(current.width) * int64(current.height)
		header := append([]byte(nil), chunks[0].data...)
		binary.BigEndian.PutUint32(header[:4], uint32(current.width))
		binary.BigEndian.PutUint32(header[4:8], uint32(current.height))
		var encoded bytes.Buffer
		encoded.WriteString("\x89PNG\r\n\x1a\n")
		appendPNGChunk(&encoded, "IHDR", header)
		for _, chunk := range common {
			appendPNGChunk(&encoded, chunk.kind, chunk.data)
		}
		appendPNGChunk(&encoded, "IDAT", current.data)
		appendPNGChunk(&encoded, "IEND", nil)
		decoded, err := imageDecodePNG(ctx, encoded.Bytes())
		if err != nil {
			return err
		}
		alpha = alpha || hasAlpha(decoded)
		frames++
		current = nil
		return nil
	}
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return false, false, err
		}
		switch chunk.kind {
		case "acTL":
			if seenAnimation || seenIDAT || len(chunk.data) != 8 {
				return false, false, ErrUnsupportedSource
			}
			declaration = binary.BigEndian.Uint32(chunk.data[:4])
			if declaration == 0 || declaration > maxImageFrames {
				return false, false, ErrBudget
			}
			seenAnimation = true
		case "PLTE", "tRNS":
			if !seenIDAT {
				common = append(common, chunk)
			}
		case "fcTL":
			if !seenAnimation || len(chunk.data) != 26 {
				return false, false, ErrUnsupportedSource
			}
			if err := finish(); err != nil {
				return false, false, err
			}
			if binary.BigEndian.Uint32(chunk.data[:4]) != sequence {
				return false, false, ErrUnsupportedSource
			}
			sequence++
			width, height := binary.BigEndian.Uint32(chunk.data[4:8]), binary.BigEndian.Uint32(chunk.data[8:12])
			x, y := binary.BigEndian.Uint32(chunk.data[12:16]), binary.BigEndian.Uint32(chunk.data[16:20])
			if width == 0 || height == 0 || uint64(x)+uint64(width) > uint64(canvasW) || uint64(y)+uint64(height) > uint64(canvasH) || chunk.data[24] > 2 || chunk.data[25] > 1 {
				return false, false, ErrUnsupportedSource
			}
			current = &pngFrame{width: int(width), height: int(height), first: !seenIDAT}
		case "IDAT":
			seenIDAT = true
			if current != nil {
				if !current.first {
					return false, false, ErrUnsupportedSource
				}
				current.data = append(current.data, chunk.data...)
			}
		case "fdAT":
			if current == nil || current.first || len(chunk.data) < 4 || binary.BigEndian.Uint32(chunk.data[:4]) != sequence {
				return false, false, ErrUnsupportedSource
			}
			sequence++
			current.data = append(current.data, chunk.data[4:]...)
		}
	}
	if err := finish(); err != nil {
		return false, false, err
	}
	if seenAnimation && uint32(frames) != declaration {
		return false, false, ErrUnsupportedSource
	}
	return seenAnimation, alpha, nil
}

type pngFrame struct {
	width, height int
	first         bool
	data          []byte
}

func imageDecodePNG(ctx context.Context, data []byte) (image.Image, error) {
	img, format, err := image.Decode(&contextReader{ctx, bytes.NewReader(data)})
	if err != nil || format != "png" {
		return nil, ErrUnsupportedSource
	}
	return img, nil
}

func riffChunks(data []byte) ([]imageChunk, error) {
	var chunks []imageChunk
	for offset := 0; offset < len(data); {
		if len(chunks) >= maxImageChunks || len(data)-offset < 8 {
			return nil, ErrUnsupportedSource
		}
		n := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if n > uint64(len(data)-offset-8) {
			return nil, ErrUnsupportedSource
		}
		end := offset + 8 + int(n)
		padded := end + int(n%2)
		if padded > len(data) || padded > end && data[end] != 0 {
			return nil, ErrUnsupportedSource
		}
		chunks = append(chunks, imageChunk{string(data[offset : offset+4]), data[offset+8 : end]})
		offset = padded
	}
	return chunks, nil
}
func uint24(data []byte) int { return int(data[0]) | int(data[1])<<8 | int(data[2])<<16 }
func frameWebP(chunks []byte, width, height int, alpha bool) []byte {
	var output bytes.Buffer
	output.WriteString("RIFF")
	output.Write(make([]byte, 4))
	output.WriteString("WEBP")
	if alpha {
		output.WriteString("VP8X")
		var length [4]byte
		binary.LittleEndian.PutUint32(length[:], 10)
		output.Write(length[:])
		header := make([]byte, 10)
		header[0] = 0x10
		for i := 0; i < 3; i++ {
			header[4+i] = byte((width - 1) >> (8 * i))
			header[7+i] = byte((height - 1) >> (8 * i))
		}
		output.Write(header)
	}
	output.Write(chunks)
	data := output.Bytes()
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	return data
}
func decodeWebP(ctx context.Context, data []byte, width, height int, budget int64) (bool, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "webp" || config.Width != width || config.Height != height {
		return false, ErrUnsupportedSource
	}
	if !pixelFit(width, height, budget) {
		return false, ErrBudget
	}
	decoded, _, err := image.Decode(&contextReader{ctx, bytes.NewReader(data)})
	if err != nil {
		return false, ErrUnsupportedSource
	}
	return hasAlpha(decoded), nil
}
func inspectWebP(ctx context.Context, data []byte, budget int64) (transformv1.SourceInfo, error) {
	if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return transformv1.SourceInfo{}, ErrUnsupportedSource
	}
	chunks, err := riffChunks(data[12:])
	if err != nil {
		return transformv1.SourceInfo{}, err
	}
	info := transformv1.SourceInfo{MIME: "image/webp", Size: int64(len(data))}
	extended, animHeader := false, false
	frames := 0
	pixels := int64(0)
	bitstreams := 0
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return info, err
		}
		switch chunk.kind {
		case "VP8X":
			if i != 0 || extended || len(chunk.data) != 10 || chunk.data[0]&0xc1 != 0 || chunk.data[1] != 0 || chunk.data[2] != 0 || chunk.data[3] != 0 {
				return info, ErrUnsupportedSource
			}
			extended = true
			info.Animated = chunk.data[0]&2 != 0
			info.Width, info.Height = 1+uint24(chunk.data[4:7]), 1+uint24(chunk.data[7:10])
			if !pixelFit(info.Width, info.Height, budget) {
				return info, ErrBudget
			}
		case "ANIM":
			if !info.Animated || animHeader || len(chunk.data) != 6 {
				return info, ErrUnsupportedSource
			}
			animHeader = true
			info.HasAlpha = info.HasAlpha || chunk.data[3] != 255
		case "ANMF":
			if !info.Animated || !animHeader || len(chunk.data) < 16 || frames >= maxImageFrames {
				return info, ErrUnsupportedSource
			}
			x, y := 2*uint24(chunk.data[:3]), 2*uint24(chunk.data[3:6])
			width, height := 1+uint24(chunk.data[6:9]), 1+uint24(chunk.data[9:12])
			if x+width > info.Width || y+height > info.Height || chunk.data[15]&0xfc != 0 {
				return info, ErrUnsupportedSource
			}
			if !pixelFit(width, height, budget-pixels) {
				return info, ErrBudget
			}
			pixels += int64(width) * int64(height)
			parts, err := riffChunks(chunk.data[16:])
			if err != nil {
				return info, err
			}
			alpha := false
			bits := 0
			for _, part := range parts {
				switch part.kind {
				case "ALPH":
					if alpha {
						return info, ErrUnsupportedSource
					}
					alpha = true
				case "VP8 ", "VP8L":
					bits++
				default:
					continue
				}
			}
			if bits != 1 {
				return info, ErrUnsupportedSource
			}
			actualAlpha, err := decodeWebP(ctx, frameWebP(chunk.data[16:], width, height, alpha), width, height, budget)
			if err != nil {
				return info, err
			}
			info.HasAlpha = info.HasAlpha || actualAlpha
			frames++
		case "VP8 ", "VP8L":
			bitstreams++
		}
	}
	if info.Animated {
		if frames == 0 || bitstreams != 0 {
			return info, ErrUnsupportedSource
		}
		return info, ctx.Err()
	}
	if bitstreams != 1 || animHeader {
		return info, ErrUnsupportedSource
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "webp" {
		return info, ErrUnsupportedSource
	}
	if extended && (config.Width != info.Width || config.Height != info.Height) {
		return info, ErrUnsupportedSource
	}
	info.Width, info.Height = config.Width, config.Height
	alpha, err := decodeWebP(ctx, data, info.Width, info.Height, budget)
	if err != nil {
		return info, err
	}
	info.HasAlpha = info.HasAlpha || alpha
	return info, ctx.Err()
}

var _ io.Reader = (*contextReader)(nil)
