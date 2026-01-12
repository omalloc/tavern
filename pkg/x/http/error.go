package http

import (
	"net/http"
)

type BizError interface {
	error

	Code() int
	Headers() http.Header
}

type bizError struct {
	code    int
	headers http.Header
}

func NewBizError(code int, headers http.Header) error {
	if headers == nil {
		headers = make(http.Header)
	}

	return &bizError{
		code:    code,
		headers: headers,
	}
}

func (e *bizError) Error() string {
	return http.StatusText(e.code)
}

func (e *bizError) Code() int {
	return e.code
}

func (e *bizError) Headers() http.Header {
	return e.headers
}

func ParseBizError(err error) (BizError, bool) {
	e, ok := err.(*bizError)
	return e, ok
}
