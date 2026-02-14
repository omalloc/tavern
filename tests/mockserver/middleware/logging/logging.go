package logging

import (
	"fmt"
	"log"
	"net/http"

	"github.com/samber/lo"
)

func Logging(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.Method, r.URL.String(), lo.Ternary(r.Header.Get("Range") != "", fmt.Sprintf("Range=%s", r.Header.Get("Range")), ""))

		next.ServeHTTP(w, r)
	}
}
