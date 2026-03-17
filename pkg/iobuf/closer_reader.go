package iobuf

import "io"

type AllCloser []io.ReadCloser

func (rc AllCloser) Close() error {
	for _, r := range rc {
		_ = r.Close()
	}
	return nil
}

type nopCloser struct {
}

func NopCloser() io.Closer {
	return &nopCloser{}
}

func (nopCloser) Close() error {
	return nil
}
