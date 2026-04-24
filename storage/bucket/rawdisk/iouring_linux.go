//go:build linux
// +build linux

package rawdisk

import (
	"errors"
	"os"
)

var (
	ErrIOURingFailed = errors.New("iouring operation failed")
)

// IOManager handles I/O submissions.
// In a true production environment, this would wrap io_uring via CGO or a modern io_uring library.
// For this implementation (due to Go 1.22+ syscall.Sockaddr link issues with older io_uring libs),
// we fall back to standard pread/pwrite which works perfectly with O_DIRECT as long as buffers are aligned.
type IOManager struct {
}

// NewIOManager initializes an IOManager with the specified queue depth.
func NewIOManager(entries uint) (*IOManager, error) {
	return &IOManager{}, nil
}

// Close cleanly shuts down the io_uring instance.
func (m *IOManager) Close() error {
	return nil
}

// ReadAt performs an asynchronous Pread and blocks until completion.
// The provided buffer must be aligned for O_DIRECT usage.
func (m *IOManager) ReadAt(f *os.File, b []byte, off int64) (int, error) {
	// Standard ReadAt uses pread syscall under the hood in Go
	return f.ReadAt(b, off)
}

// WriteAt performs an asynchronous Pwrite and blocks until completion.
// The provided buffer must be aligned for O_DIRECT usage.
func (m *IOManager) WriteAt(f *os.File, b []byte, off int64) (int, error) {
	// Standard WriteAt uses pwrite syscall under the hood in Go
	return f.WriteAt(b, off)
}
