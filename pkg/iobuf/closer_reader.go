package iobuf

import "io"

type AllCloser []io.ReadCloser

func (rc AllCloser) Close() error {
	for _, r := range rc {
		_ = r.Close()
	}
	return nil
}
