package iobuf

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSavepartCloseInterruptsReadAndJoinsCallbacks(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "async"}[async], func(t *testing.T) {
			source, producer := io.Pipe()
			defer producer.Close()
			var calls, closed atomic.Int32
			success := func([]byte, uint32, uint64, bool) error { calls.Add(1); return nil }
			var reader io.ReadCloser
			if async {
				reader = SavepartAsyncReader(source, 4, 0, success, func(error) {}, func(bool) { closed.Add(1) }, 1)
			} else {
				reader = SavepartReader(source, 4, 0, success, func(error) {}, func(bool) { closed.Add(1) })
			}
			started := make(chan struct{})
			readDone := make(chan struct{})
			go func() { close(started); io.Copy(io.Discard, reader); close(readDone) }()
			<-started
			var closes sync.WaitGroup
			for range 3 {
				closes.Go(func() { reader.Close() })
			}
			done := make(chan struct{})
			go func() { closes.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Close failed to interrupt Read")
			}
			<-readDone
			if closed.Load() != 1 {
				t.Fatal("close callback repeated", closed.Load())
			}
			before := calls.Load()
			reader.Close()
			if calls.Load() != before {
				t.Fatal("late writer callback")
			}
		})
	}
}

func TestAsyncCloseInterruptsOriginAndJoinsFetch(t *testing.T) {
	source, producer := io.Pipe()
	defer producer.Close()
	returned := make(chan struct{})
	reader := AsyncReadCloser(func() (*http.Response, error) { close(returned); return &http.Response{Body: source}, nil })
	<-returned
	done := make(chan struct{})
	go func() { reader.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("async fetch still holds its origin body")
	}
	if n, err := producer.Write([]byte("late")); n != 0 || err == nil {
		t.Fatal("origin body still open", n, err)
	}
}
