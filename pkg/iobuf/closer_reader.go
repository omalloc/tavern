package iobuf

import "io"

// AllCloser is a slice of io.ReadCloser that closes every element in order when its
// Close method is called. Individual close errors are silently discarded — this is a
// best-effort cleanup helper suitable for defer blocks where partial failure should
// not prevent the remaining resources from being released.
type AllCloser []io.ReadCloser

func (rc AllCloser) Close() error {
	for _, r := range rc {
		_ = r.Close()
	}
	return nil
}

// nopCloser implements io.Closer with a no-op Close method. Use [NopCloser] to
// satisfy an io.Closer parameter when no real cleanup is needed.
type nopCloser struct {
}

// NopCloser returns an io.Closer whose Close method does nothing.
func NopCloser() io.Closer {
	return &nopCloser{}
}

func (nopCloser) Close() error {
	return nil
}
