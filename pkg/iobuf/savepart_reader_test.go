package iobuf_test

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/omalloc/tavern/pkg/iobuf"
)

func flushBuffer(t *testing.T, fp string) iobuf.EventSuccess {
	return func(buf []byte, part uint32, pos uint64, eof bool) error {
		fp := fmt.Sprintf("%s-%06d", fp, part)
		t.Logf("buflen: %d, part: %d, currentPos: %d, path: %s", len(buf), part, pos, fp)
		return nil
	}
}

func flushBufferWithFile(_ *testing.T, f *os.File) iobuf.EventSuccess {
	return func(buf []byte, bitIdx uint32, pos uint64, eof bool) error {
		_, err := f.Write(buf)
		return err
	}
}

func flushFailed(t *testing.T) iobuf.EventError {
	return func(err error) {
		t.Fatalf("write resp body to disk failed, err=%s", err)
	}
}

func TestSavepartReaderWithRange(t *testing.T) {
	in := filepath.Join(t.TempDir(), "2mb")
	outfile1 := filepath.Join(t.TempDir(), "2mb.part")

	start, end := 500000, 2097150

	// 2MB
	fileBytes := markbuf(2 << 20)
	_ = os.WriteFile(in, fileBytes, 0o644)

	r := iobuf.SavepartReader(
		io.NopCloser(bytes.NewReader(fileBytes)),
		1024*1024,
		1024*32,
		flushBuffer(t, in), flushFailed(t), func(_ bool) {},
	)

	rr := iobuf.RangeReader(r, 0, len(fileBytes), start, end)

	f, err := os.OpenFile(outfile1, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
		return
	}

	hash1 := checksum(io.NopCloser(bytes.NewReader(fileBytes)), int64(start), int64(end))

	// dd if=/tmp/2mb of=/tmp/CCC bs=1 skip=500000 count=1597152
	sent, err := io.Copy(f, rr)
	if err != nil {
		t.Fatal(err)
		return
	}

	t.Logf("sent: %d, got: %d", sent, end-start+1)

	f2, err1 := os.OpenFile(outfile1, os.O_RDONLY, 0o644)
	if err1 != nil {
		t.Fatal(err1)
		return
	}

	h := md5.New()
	_, _ = io.Copy(h, f2)
	hash2 := hex.EncodeToString(h.Sum(nil))

	if strings.EqualFold(hash1, hash2) {
		t.Logf("md5sum: %s equal %s", hash1, hash2)
	} else {
		t.Fatalf("md5sum not equal, %s != %s", hash1, hash2)
	}
}

func TestSavepartReader(t *testing.T) {
	in := filepath.Join(t.TempDir(), "2mb")
	outfile1 := filepath.Join(t.TempDir(), "2mb.part")

	// 2MB
	fileBytes := markbuf(2 << 20)
	_ = os.WriteFile(in, fileBytes, 0o644)

	f, err := os.OpenFile(outfile1, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
		return
	}

	r := iobuf.SavepartReader(
		io.NopCloser(bytes.NewReader(fileBytes)),
		1024*1024,
		0,
		flushBufferWithFile(t, f), flushFailed(t), func(_ bool) {
			if f != nil {
				_ = f.Close()
			}
		},
	)

	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Logf("copy fail %s", err)
	}
	t.Logf("nn = %d", n)

	f2, err := os.OpenFile(outfile1, os.O_RDONLY, 0o644)
	if err != nil {
		t.Fatal(err)
		return
	}

	h := md5.New()
	_, _ = bytes.NewReader(fileBytes).WriteTo(h)
	hash1 := hex.EncodeToString(h.Sum(nil))

	h.Reset()
	nn, _ := bufio.NewReader(f2).WriteTo(h)
	hash2 := hex.EncodeToString(h.Sum(nil))
	t.Logf("read cache-file n = %d", nn)

	assert.EqualValues(t, hash1, hash2)
}

type errReader struct{ err error }

func (e *errReader) Read(p []byte) (n int, err error) { return 0, e.err }
func (e *errReader) Close() error                     { return nil }

func TestSavepartReader_Empty(t *testing.T) {
	r := iobuf.SavepartReader(
		io.NopCloser(bytes.NewReader([]byte{})),
		0,
		1024,
		func(buf []byte, bitIdx uint32, pos uint64, eof bool) error {
			t.Error("should not call onSuccess for empty reader")
			return nil
		},
		func(err error) {
			t.Errorf("should not call onError: %v", err)
		},
		func(eof bool) {
			assert.True(t, eof)
		},
	)

	n, err := io.Copy(io.Discard, r)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n)
	_ = r.Close()
}

func TestSavepartReader_ReadError(t *testing.T) {
	expectedErr := errors.New("read error")
	r := iobuf.SavepartReader(
		&errReader{err: expectedErr},
		0,
		1024,
		func(buf []byte, bitIdx uint32, pos uint64, eof bool) error {
			t.Error("should not call onSuccess")
			return nil
		},
		func(err error) {
			assert.Equal(t, expectedErr, err)
		},
		func(eof bool) {
		},
	)

	_, err := io.Copy(io.Discard, r)
	assert.ErrorIs(t, err, expectedErr)
}

func TestSavepartReader_CallbackError(t *testing.T) {
	data := markbuf(1 << 20)
	callbackErr := errors.New("callback error")

	r := iobuf.SavepartReader(
		io.NopCloser(bytes.NewReader(data)),
		1024,
		0, // small block size to trigger flush
		func(buf []byte, bitIdx uint32, pos uint64, eof bool) error {
			return callbackErr
		},
		func(err error) {
			assert.ErrorIs(t, err, callbackErr)
		},
		func(eof bool) {},
	)

	_, err := io.Copy(io.Discard, r)
	assert.ErrorIs(t, err, callbackErr)
}

func TestSavepartReader_Close(t *testing.T) {
	closed := false
	r := iobuf.SavepartReader(
		io.NopCloser(bytes.NewReader([]byte{})),
		0,
		1024,
		nil, nil,
		func(eof bool) {
			closed = true
		},
	)
	_ = r.Close()
	assert.True(t, closed)
}
