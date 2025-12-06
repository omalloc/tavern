package http

import (
	"net/http"
)

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

func IsBizError(err error) bool {
	_, ok := err.(*bizError)
	return ok
}
