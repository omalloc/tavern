package cachecontrol

import (
	"net/http"

	"github.com/omalloc/tavern/pkg/x/http/cachecontrol"
)

func CacheControl(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cc := cachecontrol.Parse(r.Header.Get("Cache-Control"))

		if cc.Cacheable() {
			w.Header().Set("Cache-Control", r.Header.Get("Cache-Control"))
		}

		next.ServeHTTP(w, r)
	}
}
