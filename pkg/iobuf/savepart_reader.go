package iobuf

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// _ is a compile-time check to ensure that savepartReader implements the io.ReadCloser interface.
var _ io.ReadCloser = (*savepartReader)(nil)

// EventSuccess defines a callback for successful events with data buffer, bit index, position, and EOF flag as parameters.
// EventError defines a callback to handle errors passed as an argument.
// EventClose defines a callback invoked when the process is closed, providing an EOF flag.
type (
	EventSuccess func(buf []byte, bitIdx uint32, pos uint64, eof bool) error
	EventError   func(err error)
	EventClose   func(eof bool)
)

// savepartReader is a wrapper around io.ReadCloser that processes data in fixed-size blocks and emits events during reading.
type savepartReader struct {
	R io.ReadCloser

	skip      bool
	eof       bool
	closed    bool
	pos       uint64
	blockSize uint64
	buf       *bytes.Buffer

	// events
	onSuccess EventSuccess
	onError   EventError
	onClose   EventClose
}

// Read reads data into p from the underlying io.ReadCloser and processes it with internal buffers and events.
func (s *savepartReader) Read(p []byte) (n int, err error) {
	n, err = s.R.Read(p)
	if err != nil {
		if errors.Is(err, io.EOF) {
			// flush buffer on EOF
			if err = s.flush(p, n, true); err != nil {
				s.onError(err)
			}
			return n, io.EOF
		}

		// trigger event error
		s.onError(err)
		return 0, err
	}

	if err = s.flush(p, n, false); err != nil {
		s.onError(err)
		return
	}
	return
}

// Close closes the underlying reader and triggers the onClose event if defined. Returns any error from the reader's Close method.
func (s *savepartReader) Close() error {
	if s.onClose != nil && !s.closed {
		s.onClose(s.eof)
		s.closed = true
	}
	return s.R.Close()
}

// flush processes provided data, manages buffer writes, handles skip logic, and writes remaining data on EOF.
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
				remaining = datalen - datapos
				s.pos += skip
				continue
			}
			s.skip = false
			continue
		}

		// full block writenow
		if uint64(s.buf.Len()) == s.blockSize {
			if err := s.writeBlock(eof); err != nil {
				return err
			}
		}

		tow := min(to, remaining)
		oldBufLen := uint64(s.buf.Len())

		s.buf.Write(data[datapos : datapos+tow])
		if oldBufLen+tow != uint64(s.buf.Len()) {
			return fmt.Errorf("partial copy - expected buffer len to be %d but it is %d",
				oldBufLen+tow, s.buf.Len())
		}

		datapos += tow
		s.pos += tow
		remaining = datalen - datapos
	}

	if eof && remaining == 0 {
		if err := s.writeBlock(eof); err != nil {
			return err
		}
	}

	return nil
}

// writeBlock writes the current buffer's contents to the destination and triggers the success event.
// It resets the buffer after writing. If the buffer is empty, no action is taken.
// The eof parameter indicates if the end of the input stream has been reached.
func (s *savepartReader) writeBlock(eof bool) error {
	s.eof = eof

	buflen := uint64(s.buf.Len())
	if buflen <= 0 {
		return nil
	}

	// calc bitmap pos
	mod := uint32((s.pos - buflen) / s.blockSize)

	// trigger event success
	if err := s.onSuccess(s.buf.Bytes(), mod, s.pos, eof); err != nil {
		return fmt.Errorf("savepart_onSuccess err %w", err)
	}

	s.buf.Reset()
	return nil
}

// SavepartReader wraps an io.ReadCloser and allows controlled, buffered reading with custom event handling for specific cases.
// Parameters:
// - r: Source io.ReadCloser to read data from.
// - blockSize: Size of each data block to buffer before triggering an event.
// - startAt: Initial offset in bytes to start reading from.
// - flushBuffer: Callback triggered when a block is successfully written.
// - flushFailed: Callback triggered if an error occurs during block processing.
// - cleanup: Callback executed when the reader is closed.
func SavepartReader(r io.ReadCloser, blockSize uint64, startAt uint,
	flushBuffer EventSuccess, flushFailed EventError, cleanup EventClose) io.ReadCloser {
	skip := false
	if startAt > 0 {
		skip = true
	}
	return &savepartReader{
		R: r,

		skip:      skip,
		pos:       uint64(startAt),
		blockSize: blockSize,
		onSuccess: flushBuffer,
		onError:   flushFailed,
		onClose:   cleanup,
		buf:       bytes.NewBuffer(make([]byte, 0, blockSize)),
	}
}
