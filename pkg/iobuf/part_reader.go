package iobuf

import (
	"errors"
	"io"
)

// partsReader concatenates multiple io.ReadCloser instances into a single sequential
// read stream. When one part is exhausted (io.EOF), it is automatically closed and
// reading continues from the next part.
//
// If closing a part returns an error, the reader stops immediately with that error
// to avoid sending corrupted data to the client. The closer field (when non-nil) is
// invoked after all parts are closed.
type partsReader struct {
	R      []io.ReadCloser
	closer io.Closer
	index  int
}

// PartsReadCloser stitches multiple io.ReadCloser instances into a single
// io.ReadCloser. Readers are consumed sequentially; each is closed when fully read.
// An optionalCloser (may be nil) is called once during the final Close.
//
// Returns nil when readers is empty.
//
// This is the central combinator used by the caching middleware to join cached chunk
// files and a live upstream reader into one continuous body for the HTTP client.
func PartsReadCloser(optionalCloser io.Closer, readers ...io.ReadCloser) io.ReadCloser {
	// if no more reader
	if len(readers) <= 0 {
		return nil
	}

	return &partsReader{
		R:      readers,
		closer: optionalCloser,
	}
}

// Read reads from the current part. On io.EOF it closes the current part (propagating
// any close error) and advances to the next part, returning nil error unless it was
// the last part.
func (r *partsReader) Read(p []byte) (n int, err error) {
	if r.index == len(r.R) {
		return 0, io.EOF
	}

	size, err := r.R[r.index].Read(p)
	if err != nil {
		if err != io.EOF {
			return size, err
		}
		// If a part response fails, next part responses should be stopped immediately;
		// otherwise, the client will receive bad file content.
		if closeErr := r.R[r.index].Close(); closeErr != nil {
			r.index++
			return size, closeErr
		}
		r.index++
		if r.index != len(r.R) {
			err = nil
		}
	}

	return size, err
}

// WriteTo implements io.WriterTo, using io.Copy on each remaining part for efficient
// forwarding (avoids an extra copy through the Read buffer).
func (r *partsReader) WriteTo(w io.Writer) (n int64, err error) {
	if r.index == len(r.R) {
		return 0, nil
	}

	var (
		nn  int64
		rrs = r.R[r.index:]
	)

	for _, reader := range rrs {
		nn, err = io.Copy(w, reader)
		n += nn

		r.index++
		if closeErr := reader.Close(); closeErr != nil {
			return n, closeErr
		}

		if err != nil {
			if err != io.EOF {
				return n, err
			}
			return
		}

		if r.index == len(r.R) {
			err = io.EOF
		}
	}
	return
}

// Close closes every remaining open part and the optional closer, collecting errors
// with errors.Join. The index tracking ensures parts already closed by Read/WriteTo
// are not double-closed.
func (r *partsReader) Close() error {
	var errs []error
	for ; r.index < len(r.R); r.index++ {
		// 如果 reader 为 nil, 则不需要关闭; L#36 处理了在异常的时候关闭 reader 的情况
		if reader := r.R[r.index]; reader != nil {
			if err := reader.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if r.closer != nil {
		if err := r.closer.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) <= 0 {
		return nil
	}

	return errors.Join(errs...)
}
