package iobuf

import "io"

// rangeReader reads exactly the byte sub-range [rawStart, rawEnd] from an underlying
// io.ReadCloser. Data before rawStart is discarded (via io.CopyN to io.Discard);
// data after rawEnd up to newEnd is also discarded, so the caller sees only the
// requested window.
//
// It is used by the caching middleware to serve HTTP Range requests from a full
// cached file — only the requested bytes are sent to the client; the rest is skipped.
type rangeReader struct {
	R        io.ReadCloser
	newStart int
	newEnd   int
	rawStart int
	rawEnd   int
	offset   int
}

// RangeReader returns an io.ReadCloser that extracts the byte range [rawStart, rawEnd]
// from r. The caller typically sets newStart=0, newEnd=(fileSize-1) and passes the
// HTTP Range's start/end as rawStart/rawEnd.
func RangeReader(r io.ReadCloser, newStart int, newEnd int, rawStart int, rawEnd int) io.ReadCloser {
	return &rangeReader{
		R:        r,
		newStart: newStart,
		newEnd:   newEnd,
		rawStart: rawStart,
		rawEnd:   rawEnd,
		offset:   newStart,
	}
}

// Read skips to rawStart on first call, reads up to len(p) bytes, and discards any
// bytes beyond rawEnd before returning io.EOF.
func (r *rangeReader) Read(p []byte) (int, error) {
	// Skip to the start of the requested range.
	if r.offset < r.rawStart {
		skipN, err := io.CopyN(io.Discard, r.R, int64(r.rawStart-r.offset))
		if err != nil {
			return 0, err
		}
		r.offset += int(skipN)
	}

	n, err := r.R.Read(p)

	// If we've read past the end of the requested range, trim and discard the tail.
	cur := r.offset + n
	if cur > r.rawEnd {
		remaining := r.rawEnd - r.offset + 1
		discardSize := min(r.newEnd, r.newEnd-cur+1)
		if discardSize > 0 {
			skipN, _ := io.CopyN(io.Discard, r.R, int64(discardSize))
			r.offset += int(skipN)
		} else {
			n += discardSize
		}
		r.offset += n
		return remaining, io.EOF
	}

	r.offset += n
	return n, err
}

// Close closes the underlying reader.
func (r *rangeReader) Close() error {
	return r.R.Close()
}
