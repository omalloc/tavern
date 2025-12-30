package cachecontrol

import (
	"log"
	"net/http"

	"github.com/omalloc/tavern/pkg/x/http/cachecontrol"
)

func CacheControl(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cc := cachecontrol.Parse(r.Header.Get("Cache-Control"))

		log.Printf("cache-control set %#+v", cc)

		w.Header().Set("Cache-Control", r.Header.Get("Cache-Control"))

		next.ServeHTTP(w, r)
	}
}
