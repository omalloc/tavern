package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/andybalholm/brotli"

	"github.com/omalloc/tavern/pkg/iobuf"
	"github.com/omalloc/tavern/tests/mockserver/middleware/cachecontrol"
	"github.com/omalloc/tavern/tests/mockserver/middleware/logging"
)

const bufSize = 10 << 20 // 10M

var (
	flagPort int
	buf      = make([]byte, bufSize)
)

func init() {
	flag.IntVar(&flagPort, "p", 8000, "usage port")

	log.SetPrefix(fmt.Sprintf("mockserver(%d): ", os.Getpid()))

	_, _ = rand.Read(buf)
}

func main() {
	flag.Parse()

	mux := http.NewServeMux()

	mux.Handle("/path/to/", http.StripPrefix("/path/to", http.FileServer(http.Dir("./files"))))

	mux.Handle("/path/", http.StripPrefix("/path/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./files/1B.bin")
	})))

	mux.Handle("/varytest/chunked", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding")

		encoding := r.Header.Get("Accept-Encoding")
		if strings.Contains(encoding, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			nw := gzip.NewWriter(w)
			defer nw.Close()

			r := iobuf.NewRateLimitReader(io.NopCloser(bytes.NewReader(buf)), 100)
			_, _ = io.Copy(nw, r)

			return
		}

		if strings.Contains(encoding, "br") {
			w.Header().Set("Content-Encoding", "br")
			nw := brotli.NewWriter(w)
			defer nw.Close()

			r := iobuf.NewRateLimitReader(io.NopCloser(bytes.NewReader(buf)), 100)
			_, _ = io.Copy(nw, r)

			return
		}

		_, _ = w.Write([]byte("hello world"))
	}))

	mux.Handle("/varytest/normal", http.StripPrefix("/varytest/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "User-Agent")

		http.ServeFile(w, r, "./files/1M.bin")
	})))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("received request: %s %s", r.Method, r.URL.String())

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	})

	addr := fmt.Sprintf(":%d", flagPort)

	log.Printf("HTTP server listener on %s", addr)
	if err := http.ListenAndServe(addr, logging.Logging(cachecontrol.CacheControl(mux))); err != nil {
		return
	}
}
