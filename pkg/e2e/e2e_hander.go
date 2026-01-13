package e2e

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func WrongHit(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)

		assert.Error(t, errors.New("equal request HIT but NOT HIT"))
	}
}

func RespSimpleFile(f *MockFile) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=10")
		w.Header().Set("Content-MD5", f.MD5)
		w.Header().Set("ETag", f.MD5)
		w.Header().Set("X-Server", "tavern-e2e/1.0.0")

		http.ServeFile(w, r, f.Path)
	}
}

func RespCallbackFile(f *MockFile, cb func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=10")
		w.Header().Set("Content-MD5", f.MD5)
		w.Header().Set("ETag", f.MD5)
		w.Header().Set("X-Server", "tavern-e2e/1.0.0")

		cb(w, r)

		http.ServeFile(w, r, f.Path)
	}
}

func RespCallback(cb func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cb(w, r)
	}
}
