package iobuf

import "io"

// limitedReadCloser wraps an io.ReadCloser and imposes a maximum byte limit via
// io.LimitReader. It tracks the total bytes read in the n field for diagnostics.
// It also exposes a WriteTo method that uses io.Copy for zero-copy forwarding
// (useful when the reader serves an HTTP response body).
type limitedReadCloser struct {
	R       io.ReadCloser
	limited io.Reader
	max     int64
	n       int
}

// LimitReadCloser returns an io.ReadCloser that reads at most max bytes from the
// underlying reader before returning io.EOF. It is used in the caching middleware
// to cap the response body to the advertised Content-Length (or a range length),
// preventing over-read.
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
