package storage

import (
	"io"
	"os"
)

// File is a readable, writable sequence of bytes.
//
// Typically, it will be an *os.File, but test code may choose to substitute
// memory-backed implementations.
//
// Write-oriented operations (Write, Sync) must be called sequentially: At most
// 1 call to Write or Sync may be executed at any given time.
type File interface {
	io.Closer
	io.Reader
	io.ReaderAt
	// Unlike the specification for io.Writer.Write(), the vfs.File.Write()
	// method *is* allowed to modify the slice passed in, whether temporarily
	// or permanently. Callers of Write() need to take this into account.
	io.Writer
	// WriteAt() is only supported for files that were opened with FS.OpenReadWrite.
	io.WriterAt

	// Preallocate optionally preallocates storage for `length` at `offset`
	// within the file. Implementations may choose to do nothing.
	Stat() (os.FileInfo, error)
	Sync() error

	// Fd returns the raw file descriptor when a File is backed by an *os.File.
	// It can be used for specific functionality like Prefetch.
	// Returns InvalidFd if not supported.
	Fd() uintptr
}
