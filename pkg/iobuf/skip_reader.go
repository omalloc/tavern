package iobuf

import "io"

type skipReadCloser struct {
	io.ReadCloser

	skip int64
}

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
