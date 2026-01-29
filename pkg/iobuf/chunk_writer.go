package iobuf

import (
	"io"
)

type chunkWriter struct {
	w      io.ReadWriteCloser
	closer func() error
}

func (cw *chunkWriter) Close() error {
	if err := cw.w.Close(); err != nil {
		return err
	}
	return cw.closer()
}

func (cw *chunkWriter) Write(p []byte) (n int, err error) {
	return cw.w.Write(p)
}

func ChunkWriterCloser(file io.ReadWriteCloser, closer func() error) io.WriteCloser {
	return &chunkWriter{
		w:      file,
		closer: closer,
	}
}
