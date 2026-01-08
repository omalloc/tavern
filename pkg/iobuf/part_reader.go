package iobuf

import (
	"errors"
	"io"
)

// partsReader is a composite reader that reads sequentially from a slice of io.ReadCloser instances.
// It ensures that each reader is closed when fully read or upon encountering an error.
// The closer field is an optional io.Closer for final cleanup operations after all readers are processed.
// The index field tracks the current reader being processed in the slice.
type partsReader struct {
	R      []io.ReadCloser
	closer io.Closer
	index  int
}

// PartsReadCloser combines multiple io.ReadCloser instances into a single io.ReadCloser, with optional final cleanup logic.
func PartsReadCloser(closer io.Closer, readers ...io.ReadCloser) io.ReadCloser {
	// if no more reader
	if len(readers) <= 0 {
		return nil
	}

	return &partsReader{
		R:      readers,
		closer: closer,
	}
}

// Read reads data into p from the current part, advancing to the next part on EOF and closing completed parts.
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
			return size, closeErr
		}
		r.index++
		if r.index != len(r.R) {
			err = nil
		}
	}

	return size, err
}

// WriteTo writes the remaining unread parts to the provided writer and returns the number of bytes written and any error.
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
		if err != nil {
			if closeErr := reader.Close(); closeErr != nil {
				return n, closeErr
			}

			if err != io.EOF {
				return n, err
			}
			return
		}
		r.index++
		if r.index == len(r.R) {
			err = io.EOF
		}
	}
	return
}

// Close closes all remaining open readers in the partsReader and the associated closer, aggregating any errors encountered.
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
