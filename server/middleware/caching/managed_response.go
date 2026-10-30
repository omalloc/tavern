package caching

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/internal/protocol"
	planning "github.com/omalloc/tavern/transform"
)

// Bind a complete, pinned representation before evaluating client conditions or
// ranges. ServeContent applies opaque ETags and byte ranges to these exact bytes.
func (p *managedProvider) roundTrip(req *http.Request, plan planning.Plan) (*http.Response, error) {
	result, err := p.resolve(req, plan)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		status := planning.HTTPStatus(err)
		if status == http.StatusInternalServerError {
			status = http.StatusBadGateway
		}
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		if errors.Is(err, planning.ErrBusy) {
			status = http.StatusServiceUnavailable
		}
		return planning.ErrorResponse(req, status), nil
	}

	accept, parseErr := planning.ParseAccept(plan.AcceptHeader)
	if parseErr != nil || accept.Quality(result.Metadata.Headers.Get("Content-Type")) == 0 {
		result.Close()
		return planning.ErrorResponse(req, http.StatusNotAcceptable), nil
	}
	return respondManagedResult(req, result)
}

func respondManagedResult(req *http.Request, result *managedResult) (*http.Response, error) {
	if !planning.EncodingAccepted(req.Header, result.Metadata.Headers.Get("Content-Encoding")) {
		result.Close()
		return planning.ErrorResponse(req, http.StatusNotAcceptable), nil
	}
	status := storage.CacheMiss
	if result.Metadata.ID == nil || result.Fallback {
		status = storage.BYPASS
	} else if result.Cached {
		status = storage.CacheHit
	}
	headers := result.Metadata.Headers.Clone()
	if result.Metadata.RespUnix > 0 {
		age, _ := strconv.ParseInt(headers.Get("Age"), 10, 64)
		elapsed := max(int64(0), time.Now().Unix()-result.Metadata.RespUnix)
		headers.Set("Age", strconv.FormatInt(saturatedAge(max(int64(0), age), elapsed), 10))
	}
	headers.Set(protocol.ProtocolCacheStatusKey, status.String())
	managedVary(headers)
	if req.Method == http.MethodGet && (len(req.Header.Get("Range")) > 16384 || strings.Count(req.Header.Get("Range"), ",") >= 16) {
		result.Close()
		headers.Set("Content-Range", fmt.Sprintf("bytes */%d", result.Metadata.Size))
		headers.Set("Cache-Control", "no-store")
		headers.Set("Content-Length", "0")
		return &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Header: headers, ContentLength: 0, Body: http.NoBody, Request: req}, nil
	}
	content := &managedSeeker{ctx: req.Context(), result: result, size: int64(result.Metadata.Size)}
	pr, pw := io.Pipe()
	ready := make(chan *http.Response, 1)
	done := make(chan struct{})
	writer := &managedWriter{header: headers, ready: ready, pipe: pw, request: req}
	request := req
	if req.Method == http.MethodHead {
		request = req.Clone(req.Context())
		request.Header.Del("Range")
		request.Header.Del("If-Range")
	}
	modified, _ := http.ParseTime(headers.Get("Last-Modified"))
	go func() {
		defer close(done)
		defer result.Close()
		defer content.Close()
		http.ServeContent(writer, request, "", modified, content)
		if !writer.sent {
			writer.WriteHeader(http.StatusOK)
		}
		err := content.failure()
		if writer.err != nil {
			err = errors.Join(err, writer.err)
		}
		if req.Method != http.MethodHead && writer.code != http.StatusNotModified && writer.length >= 0 && writer.written != writer.length {
			err = errors.Join(err, io.ErrUnexpectedEOF)
		}
		content.Close()
		result.Close()
		pw.CloseWithError(err)
	}()
	select {
	case response := <-ready:
		if req.Method == http.MethodHead || response.StatusCode == http.StatusNotModified || response.StatusCode == http.StatusNoContent {
			pr.Close()
			<-done
			response.Body = http.NoBody
		} else {
			response.Body = &managedPipelineBody{PipeReader: pr, done: done}
		}
		return response, nil
	case <-req.Context().Done():
		pr.CloseWithError(req.Context().Err())
		<-done
		return nil, req.Context().Err()
	}
}

// Closing a client body must wait for the local producer to release its reader
// and pin before the outer storage task can report completion during Drain.
type managedPipelineBody struct {
	*io.PipeReader
	done <-chan struct{}
	once sync.Once
	err  error
}

func (b *managedPipelineBody) Close() error {
	b.once.Do(func() {
		b.err = b.PipeReader.Close()
		<-b.done
	})
	return b.err
}

// Every route varies on both negotiation and no-transform, including origin
// preservation, fallback, errors, HEAD and 304. Existing origin fields survive.
func managedVary(header http.Header) {
	fields := strings.Join(header.Values("Vary"), ",")
	seen := map[string]bool{}
	for _, field := range strings.Split(fields, ",") {
		seen[strings.ToLower(strings.TrimSpace(field))] = true
	}
	if seen["*"] {
		return
	}
	for _, field := range []string{"Accept", "Cache-Control", "Accept-Encoding"} {
		if !seen[strings.ToLower(field)] {
			header.Add("Vary", field)
		}
	}
}

type managedWriter struct {
	header          http.Header
	ready           chan<- *http.Response
	pipe            *io.PipeWriter
	request         *http.Request
	sent            bool
	code            int
	length, written int64
	err             error
}

func (w *managedWriter) Header() http.Header { return w.header }
func (w *managedWriter) WriteHeader(code int) {
	if w.sent {
		return
	}
	w.sent, w.code = true, code
	if code >= 400 {
		w.header.Set("Cache-Control", "no-store")
	}
	if code == http.StatusPreconditionFailed {
		w.header.Set("Content-Length", "0")
	}
	w.length = -1
	if value := w.header.Get("Content-Length"); value != "" {
		w.length, _ = strconv.ParseInt(value, 10, 64)
	}
	length := w.length
	if code == http.StatusNotModified || code == http.StatusNoContent {
		length = 0
	}
	w.ready <- &http.Response{StatusCode: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code)), Header: w.header.Clone(), ContentLength: length, Request: w.request}
}
func (w *managedWriter) Write(data []byte) (int, error) {
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
	if w.request.Method == http.MethodHead {
		return len(data), nil
	}
	n, err := w.pipe.Write(data)
	w.written += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}

// A lazy seeker uses metadata for size/HEAD/304 and opens bytes only on Read.
// Cached chunk readers implement Seek, so ranges skip whole chunks directly.
type managedSeeker struct {
	ctx            context.Context
	result         *managedResult
	size, position int64
	reader         io.ReadCloser
	mu             sync.Mutex
	closed         bool
	err            error
}

var _ io.ReadSeekCloser = (*managedSeeker)(nil)

func (s *managedSeeker) Read(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if err := s.ctx.Err(); err != nil {
		s.err = err
		return 0, err
	}
	if s.position == s.size {
		return 0, io.EOF
	}
	if s.reader == nil {
		reader, err := s.result.Open(s.ctx)
		if err != nil {
			s.err = err
			return 0, err
		}
		s.reader = reader
		if s.position > 0 {
			if seeker, ok := reader.(io.Seeker); ok {
				_, err = seeker.Seek(s.position, io.SeekStart)
			} else {
				_, err = io.CopyN(io.Discard, reader, s.position)
			}
			if err != nil {
				s.err = err
				return 0, err
			}
		}
	}
	n, err := s.reader.Read(data)
	s.position += int64(n)
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err
}
func (s *managedSeeker) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	position, err := managedSeekPosition(s.position, s.size, offset, whence)
	if err != nil {
		return 0, err
	}
	if s.reader != nil {
		if seeker, ok := s.reader.(io.Seeker); ok {
			if _, err = seeker.Seek(position, io.SeekStart); err != nil {
				return 0, err
			}
		} else {
			s.reader.Close()
			s.reader = nil
		}
	}
	s.position = position
	return position, nil
}
func managedSeekPosition(current, size, offset int64, whence int) (int64, error) {
	base := int64(0)
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = current
	case io.SeekEnd:
		base = size
	default:
		return 0, errors.New("invalid seek origin")
	}
	// Restrict seeks to this complete representation and avoid integer overflow.
	if offset < -base || offset > size-base {
		return 0, errors.New("seek outside complete representation")
	}
	return base + offset, nil
}
func (s *managedSeeker) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.reader != nil {
		return s.reader.Close()
	}
	return nil
}
func (s *managedSeeker) failure() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
