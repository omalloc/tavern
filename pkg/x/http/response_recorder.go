package http

import (
	"bufio"
	"net"
	"net/http"
)

var _ http.Hijacker = (*ResponseRecorder)(nil)

type ResponseRecorder struct {
	http.Hijacker
	http.ResponseWriter

	status int
	size   uint64
}

func NewResponseRecorder(w http.ResponseWriter) *ResponseRecorder {
	return &ResponseRecorder{ResponseWriter: w}
}

func (r *ResponseRecorder) Write(b []byte) (n int, err error) {
	if r.status == 0 {
		// The status will be StatusOK if WriteHeader has not been called yet
		r.status = http.StatusOK
	}

	n, err = r.ResponseWriter.Write(b)
	if err == nil {
		r.size += uint64(n)
	}
	return n, err
}

func (r *ResponseRecorder) WriteHeader(s int) {
	r.ResponseWriter.WriteHeader(s)
	r.status = s
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker.
func (r *ResponseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrHijacked
}

func (r *ResponseRecorder) Status() int {
	return r.status
}

func (r *ResponseRecorder) Size() uint64 {
	return r.size
}

func (r *ResponseRecorder) SentBytes() uint64 {
	return ResponseHeaderSize(r.Status(), r.Header()) + r.Size()
}

func ResponseHeaderSize(code int, hdr http.Header) uint64 {
	// example: HTTP/1.1 200 OK\r\n
	n := uint64(len(http.StatusText(code))) + 15

	// headers
	// Server: nginx/1.20.1\r\n
	for k, v := range hdr {
		n += uint64(len(k) + 4)
		for _, s := range v {
			n += uint64(len(s))
		}
	}

	// \r\n
	n += 2
	return n
}
