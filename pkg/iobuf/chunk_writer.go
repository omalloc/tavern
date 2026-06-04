package iobuf

import (
	"io"
)

// chunkWriter wraps an io.ReadWriteCloser and delegates Write directly to it.
// On Close, it first closes the underlying writer and then calls a user-provided
// closer callback — typically used to finalize metadata / update the chunk index
// after the file is fully written.
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

// ChunkWriterCloser returns an io.WriteCloser that wraps a disk file (typically an
// opened chunk file). When Close is called the underlying file is closed first, then
// the closer callback is invoked — this is where the storage layer updates the LSM-tree
// index to record the new chunk.
func ChunkWriterCloser(file io.ReadWriteCloser, closer func() error) io.WriteCloser {
	return &chunkWriter{
		w:      file,
		closer: closer,
	}
}
