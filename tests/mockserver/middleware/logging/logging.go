package logging

import (
	"log"
	"net/http"
)

func Logging(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s Range=%s", r.Method, r.URL.String(), r.Header.Get("Range"))

		next.ServeHTTP(w, r)
	}
}
