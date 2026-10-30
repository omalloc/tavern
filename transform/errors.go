package transform

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// HTTPStatus maps managed-route contract failures. Unknown execution failures
// remain server errors; a failed decision can never be a cacheable image.
func HTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotAcceptable):
		return http.StatusNotAcceptable
	case errors.Is(err, ErrInvalidAccept), errors.Is(err, ErrInvalidParameter):
		return http.StatusBadRequest
	case errors.Is(err, ErrUnsupportedSource), errors.Is(err, ErrAlphaLoss), errors.Is(err, ErrAnimation):
		return http.StatusUnsupportedMediaType
	default:
		return http.StatusInternalServerError
	}
}

// ErrorResponse contains no internal diagnostics and has the same negotiation
// dimensions as successful managed responses. HEAD retains the GET length.
func ErrorResponse(req *http.Request, status int) *http.Response {
	body := http.StatusText(status)
	headers := http.Header{"Content-Type": {"text/plain; charset=utf-8"}, "Cache-Control": {"no-store"}, "Vary": {"Accept, Cache-Control, Accept-Encoding"}, "Content-Length": {strconv.Itoa(len(body))}}
	var reader io.ReadCloser = io.NopCloser(strings.NewReader(body))
	if req.Method == http.MethodHead {
		reader = http.NoBody
	}
	return &http.Response{StatusCode: status, Header: headers, ContentLength: int64(len(body)), Body: reader, Request: req}
}
