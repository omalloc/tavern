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
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"

	"github.com/omalloc/tavern/pkg/iobuf"
	"github.com/omalloc/tavern/pkg/x/http/rangecontrol"
	"github.com/omalloc/tavern/tests/mockserver/middleware/cachecontrol"
	"github.com/omalloc/tavern/tests/mockserver/middleware/logging"
)

const bufSize = 10 << 20 // 10M

var (
	flagPort int
	buf      = make([]byte, bufSize)
)

const testConfig = `{
  "type": "chash",
  "nodes": [
    {
      "url": "http://127.0.0.1:9000",
      "weight": 1,
      "enabled": true,
      "metadata": {
        "region": "xiamen",
        "zone": "a"
      }
    },
    {
      "url": "http://127.0.0.1:9001",
      "weight": 1,
      "enabled": true,
      "metadata": {
        "region": "quanzhou",
        "zone": "b"
      }
    }
  ],
  "health_check": {
    "enabled": true,
    "interval": 10,
    "timeout": 3,
    "path": "/health"
  }
}
`

func init() {
	flag.IntVar(&flagPort, "p", 8000, "usage port")

	log.SetPrefix(fmt.Sprintf("mockserver(%d): ", os.Getpid()))

	_, _ = rand.Read(buf)
}

func main() {
	flag.Parse()

	mux := http.NewServeMux()

	mux.Handle("/api/configs/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(testConfig)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(testConfig))
	}))

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

	mux.Handle("/chunked", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalSize := int64(len(buf))

		reader := io.NopCloser(bytes.NewReader(buf))
		if r.Header.Get("X-Limit") != "" {
			reader = iobuf.NewRateLimitReader(io.NopCloser(bytes.NewReader(buf)), 200)
		}

		rawRange := r.Header.Get("Range")

		byteRange, err := rangecontrol.Parse(rawRange)
		if err != nil {
			_, _ = io.Copy(w, reader)
			return
		}

		rr := byteRange[0]

		// 2. 边界检查
		if rr.Start < 0 || rr.Start >= totalSize || rr.End < rr.Start {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}

		if rr.End >= totalSize {
			rr.End = totalSize - 1
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rr.Start, rr.End, totalSize))
		w.Header().Set("Accept-Ranges", "bytes")

		w.WriteHeader(http.StatusPartialContent)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
			return
		}
		flusher.Flush()

		n, err := io.Copy(w, iobuf.LimitReadCloser(iobuf.SkipReadCloser(reader, rr.Start), rr.Length()))
		log.Printf("received request: chunked bytes %d, rawRange %s", n, rawRange)

	}))

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
