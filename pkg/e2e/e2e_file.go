package e2e

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/omalloc/tavern/pkg/iobuf"
)

type MockFile struct {
	Path string
	MD5  string
	Size int
}

func GenFile(t *testing.T, size int) *MockFile {
	buf := make([]byte, size)
	_, _ = rand.Read(buf)

	fpath := filepath.Join(t.TempDir(), fmt.Sprintf("file-%d.bin", size))
	_ = os.WriteFile(fpath, buf, 0o644)

	return &MockFile{
		Path: fpath,
		MD5:  SumMD5(buf),
		Size: size,
	}
}

func SumMD5(buf []byte) string {
	h := md5.New()
	_, _ = h.Write(buf)
	return hex.EncodeToString(h.Sum(nil))
}

func DiscardBody(resp *http.Response, readSpeedKbps int) int {
	if resp == nil || resp.Body == nil {
		return 0
	}
	if readSpeedKbps <= 0 {
		readSpeedKbps = 5 * 1024 // 5MB/s
	}

	// n, _ := io.Copy(io.Discard, resp.Body)
	n, _ := io.Copy(io.Discard, iobuf.NewRateLimitReader(resp.Body, readSpeedKbps))
	_ = resp.Body.Close()
	return int(n)
}

func HashBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}

	h := md5.New()
	_, _ = io.Copy(h, resp.Body)
	return hex.EncodeToString(h.Sum(nil))
}

func HashFile(path string, offset, length int) string {
	f, err := os.OpenFile(path, os.O_RDONLY, 0o644)
	if err != nil {
		return ""
	}
	defer f.Close()

	_, _ = f.Seek(int64(offset), io.SeekStart)

	h := md5.New()
	_, _ = io.CopyN(h, f, int64(length))
	return hex.EncodeToString(h.Sum(nil))
}

func SplitFile(path string, offset, length int) io.ReadCloser {
	f, err := os.OpenFile(path, os.O_RDONLY, 0o644)
	if err != nil {
		return nil
	}
	_, _ = f.Seek(int64(offset), io.SeekStart)
	return iobuf.RangeReader(f, offset, offset+length-1, offset, offset+length-1)
}
