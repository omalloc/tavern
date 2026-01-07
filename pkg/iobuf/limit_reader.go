package iobuf

import "io"

// limitedReadCloser is a structure that wraps an io.ReadCloser, imposing a maximum read limit and tracking read bytes.
type limitedReadCloser struct {
	R       io.ReadCloser
	limited io.Reader
	max     int64
	n       int
}

// LimitReadCloser wraps an io.ReadCloser, limiting the number of bytes that can be read from it up to a specified maximum.
func LimitReadCloser(readCloser io.ReadCloser, max int64) io.ReadCloser {
	return &limitedReadCloser{
		max:     max,
		limited: io.LimitReader(readCloser, max),
		R:       readCloser,
	}
}

// Read reads up to len(p) bytes into p from the underlying limited reader and tracks the total bytes read.
func (lrc *limitedReadCloser) Read(p []byte) (n int, err error) {
	n, err = lrc.limited.Read(p)

	lrc.n += n
	return
}

// WriteTo writes data from the limited reader to the provided writer and returns the number of bytes written and any error.
func (lrc *limitedReadCloser) WriteTo(w io.Writer) (n int64, err error) {
	n, err = io.Copy(w, lrc.limited)

	lrc.n += int(n)
	return
}

// Close releases resources associated with the underlying io.ReadCloser.
func (lrc *limitedReadCloser) Close() error {
	return lrc.R.Close()
}
