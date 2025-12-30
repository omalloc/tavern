package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/omalloc/tavern/tests/mockserver/middleware/cachecontrol"
	"github.com/omalloc/tavern/tests/mockserver/middleware/logging"
)

var (
	flagPort int
)

func init() {
	flag.IntVar(&flagPort, "p", 8000, "usage port")

	log.SetPrefix(fmt.Sprintf("mockserver(%d): ", os.Getpid()))
}

func main() {
	flag.Parse()

	mux := http.NewServeMux()

	mux.Handle("/path/to/", http.StripPrefix("/path/to", http.FileServer(http.Dir("./files"))))
	mux.Handle("/path/", http.StripPrefix("/path/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./files/1B.bin")
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
