package iobuf

import "io"

// skipReadCloser wraps an io.ReadCloser and discards skip bytes on the first Read
// before delegating to the underlying reader. If the underlying reader implements
// io.Seeker, [SkipReadCloser] prefers Seek over discarding.
type skipReadCloser struct {
	io.ReadCloser

	skip int64
}

// SkipReadCloser returns an io.ReadCloser that skips skip bytes before the first
// byte is returned to the caller. If the underlying reader supports io.Seeker,
// a single Seek call is used; otherwise bytes are discarded via io.CopyN to
// io.Discard (which may happen across multiple Read calls if the discard is
// interrupted).
//
// Used by the caching middleware to skip past leading chunk data that sits before
// the requested Range offset.
func SkipReadCloser(R io.ReadCloser, skip int64) io.ReadCloser {
	if seeker, ok := R.(io.Seeker); ok {
		_, err := seeker.Seek(skip, io.SeekCurrent)
		if err == nil {
			return R
		}
	}

	return &skipReadCloser{
		ReadCloser: R,
		skip:       skip,
	}
}

// Read discards the remaining skip bytes (when r.skip > 0), then delegates to
// the underlying reader.
func (r *skipReadCloser) Read(p []byte) (int, error) {
	if r.skip > 0 {
		if n, err := io.CopyN(io.Discard, r.ReadCloser, r.skip); err != nil {
			r.skip -= n
			return 0, err
		}
		r.skip = 0
	}

	return r.ReadCloser.Read(p)
}
