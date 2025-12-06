package bufio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

var _ io.ReadCloser = (*savepartReader)(nil)

type (
	EventSuccess func(buf []byte, bitIdx uint32, pos uint64, eof bool) error
	EventError   func(err error)
	EventClose   func(eof bool)
)

type savepartReader struct {
	R io.ReadCloser

	skip      bool
	eof       bool
	pos       uint64
	blockSize uint64
	buf       *bytes.Buffer

	// events
	onSuccess EventSuccess
	onError   EventError
	onClose   EventClose
}

// Read implements io.ReadCloser.
func (s *savepartReader) Read(p []byte) (n int, err error) {
	n, err = s.R.Read(p)
	if err != nil {
		if errors.Is(err, io.EOF) {
			// flush buffer on EOF
			return n, io.EOF
		}

		// trigger event error
		return 0, err
	}

	if err = s.flush(p, n, false); err != nil {
		return
	}
	return
}

// Close implements io.ReadCloser.
func (s *savepartReader) Close() error {
	if s.onClose != nil {
		s.onClose(s.eof)
	}
	return s.R.Close()
}

func (s *savepartReader) flush(data []byte, realLen int, eof bool) error {
	datalen := uint64(realLen)
	datapos := uint64(0)

	remaining := datalen

	for remaining > 0 {
		// calculate from and to within the block
		from := s.pos % s.blockSize
		to := s.blockSize - from

		// check if we need to skip
		// if we are at the beginning of a block and need to skip
		// we can skip the entire block
		if s.skip {
			// if we are at the beginning of 0 block and need to skip
			if from != 0 {
				skip := min(to, remaining)
				datapos += skip
				remaining -= datalen - datapos
				s.pos += skip
				continue
			}
			s.skip = false
			continue
		}

		// full block writenow
		if uint64(s.buf.Len()) == s.blockSize {
			if err := s.writeBlock(eof); err != nil {
				s.onError(err)
				return err
			}
		}

		tow := min(to, remaining)
		oldBufLen := uint64(s.buf.Len())

		s.buf.Write(data[datapos : datapos+tow])
		if oldBufLen+tow != uint64(s.buf.Len()) {
			err1 := fmt.Errorf("partial copy - expected buffer len to be %d but it is %d",
				oldBufLen+tow, s.buf.Len())
			s.onError(err1)
			return err1
		}
		datapos += tow
		s.pos += tow
		remaining = datalen - datapos
	}

	if eof && remaining == 0 {
		if err := s.writeBlock(eof); err != nil {
			s.onError(err)
			return err
		}
	}

	return nil
}

func (s *savepartReader) writeBlock(eof bool) error {
	s.eof = eof

	buflen := uint64(s.buf.Len())
	if buflen <= 0 {
		return nil
	}

	// calc bitmap pos
	mod := uint32((s.pos - buflen) / s.blockSize)

	// trigger event success
	s.onSuccess(s.buf.Bytes(), mod, s.pos, eof)

	s.buf.Reset()
	return nil
}

func SavepartReader(r io.ReadCloser, flushBuffer EventSuccess, cleanup EventClose) io.ReadCloser {
	return &savepartReader{
		R: r,

		skip:      true,
		onSuccess: flushBuffer,
		onError: func(err error) {
			// TODO: check err
		},
		onClose: cleanup,
	}
}
