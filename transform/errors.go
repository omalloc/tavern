package transform

import (
	"errors"
	"net/http"
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
