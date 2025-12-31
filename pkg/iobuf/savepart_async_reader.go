package iobuf

import (
	"bytes"
	"errors"
	"io"
	"sync"
)

// writeJob represents an async write task
type writeJob struct {
	buf    []byte
	bitIdx uint32
	pos    uint64
	eof    bool
}

// savepartAsyncReader decouples disk writes from the read path
type savepartAsyncReader struct {
	R io.ReadCloser

	skip      bool
	eof       bool
	closed    bool
	pos       uint64
	blockSize uint64
	buf       *bytes.Buffer

	// async write
	writeCh  chan writeJob
	writeWg  sync.WaitGroup
	writeErr error
	writeMu  sync.Mutex

	// events
	onSuccess EventSuccess
	onError   EventError
	onClose   EventClose
}

func (s *savepartAsyncReader) startWriter() {
	s.writeWg.Add(1)
	go func() {
		defer s.writeWg.Done()
		for job := range s.writeCh {
			if err := s.onSuccess(job.buf, job.bitIdx, job.pos, job.eof); err != nil {
				s.writeMu.Lock()
				if s.writeErr == nil {
					s.writeErr = err
				}
				s.writeMu.Unlock()
				s.onError(err)
			}
		}
	}()
}

func (s *savepartAsyncReader) Read(p []byte) (n int, err error) {
	// check for previous write errors
	s.writeMu.Lock()
	if s.writeErr != nil {
		err := s.writeErr
		s.writeMu.Unlock()
		return 0, err
	}
	s.writeMu.Unlock()

	n, err = s.R.Read(p)
	if err != nil {
		if errors.Is(err, io.EOF) {
			if flushErr := s.flush(p, n, true); flushErr != nil {
				s.onError(flushErr)
			}
			return n, io.EOF
		}
		s.onError(err)
		return 0, err
	}

	if flushErr := s.flush(p, n, false); flushErr != nil {
		s.onError(flushErr)
		return n, flushErr
	}
	return
}

func (s *savepartAsyncReader) Close() error {
	if !s.closed {
		s.closed = true
		close(s.writeCh)
		s.writeWg.Wait() // wait for pending writes
		if s.onClose != nil {
			s.onClose(s.eof)
		}
	}
	return s.R.Close()
}

func (s *savepartAsyncReader) flush(data []byte, realLen int, eof bool) error {
	datalen := uint64(realLen)
	datapos := uint64(0)
	remaining := datalen

	for remaining > 0 {
		from := s.pos % s.blockSize
		to := s.blockSize - from

		if s.skip {
			if from != 0 {
				skip := min(to, remaining)
				datapos += skip
				remaining = datalen - datapos
				s.pos += skip
				continue
			}
			s.skip = false
			continue
		}

		if uint64(s.buf.Len()) == s.blockSize {
			if err := s.enqueueWrite(eof); err != nil {
				return err
			}
		}

		tow := min(to, remaining)
		s.buf.Write(data[datapos : datapos+tow])
		datapos += tow
		s.pos += tow
		remaining = datalen - datapos
	}

	if eof && remaining == 0 {
		return s.enqueueWrite(eof)
	}
	return nil
}

func (s *savepartAsyncReader) enqueueWrite(eof bool) error {
	s.eof = eof
	buflen := s.buf.Len()
	if buflen <= 0 {
		return nil
	}

	// copy buffer for async processing
	bufCopy := make([]byte, buflen)
	copy(bufCopy, s.buf.Bytes())

	mod := uint32((s.pos - uint64(buflen)) / s.blockSize)

	select {
	case s.writeCh <- writeJob{buf: bufCopy, bitIdx: mod, pos: s.pos, eof: eof}:
	default:
		// channel full, apply backpressure or drop (depending on your requirements)
		// here we block to ensure data integrity
		s.writeCh <- writeJob{buf: bufCopy, bitIdx: mod, pos: s.pos, eof: eof}
	}

	s.buf.Reset()
	return nil
}

// SavepartAsyncReader creates an async version that decouples disk I/O from read path
func SavepartAsyncReader(r io.ReadCloser, blockSize uint64, startAt uint,
	flushBuffer EventSuccess, flushFailed EventError, cleanup EventClose,
	writeQueueSize int) io.ReadCloser {

	if writeQueueSize <= 0 {
		writeQueueSize = 16 // default buffer size
	}

	skip := startAt > 0
	reader := &savepartAsyncReader{
		R:         r,
		skip:      skip,
		pos:       uint64(startAt),
		blockSize: blockSize,
		onSuccess: flushBuffer,
		onError:   flushFailed,
		onClose:   cleanup,
		buf:       bytes.NewBuffer(make([]byte, 0, blockSize)),
		writeCh:   make(chan writeJob, writeQueueSize),
	}
	reader.startWriter()
	return reader
}
