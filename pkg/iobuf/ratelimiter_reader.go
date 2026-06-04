package iobuf

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

// rateLimitReader throttles reads from an underlying io.ReadCloser to at most the
// configured rate. It uses a token-bucket limiter (golang.org/x/time/rate). The rate
// is specified in Kbps (kilobits per second).
type rateLimitReader struct {
	R io.ReadCloser
	L *rate.Limiter
}

// NewRateLimitReader returns an io.ReadCloser that limits the read throughput to
// Kbps kilobits per second. The burst size equals one second of tokens so the
// reader can handle short spikes.
//
// Used primarily in tests (mockserver, e2e) to simulate bandwidth-constrained
// upstream servers.
func NewRateLimitReader(r io.ReadCloser, Kbps int) io.ReadCloser {
	l := Kbps << 10
	return &rateLimitReader{
		R: r,
		L: rate.NewLimiter(rate.Limit(l), l),
	}
}

// Read reads from the underlying reader in burst-sized chunks, waiting for tokens
// before each chunk. Returns when the buffer is full or the underlying reader
// returns an error.
func (r *rateLimitReader) Read(p []byte) (n int, err error) {
	l := len(p)
	ctx := context.Background()
	burst := r.L.Burst()
	for {
		size := l - n
		if size > burst {
			size = burst
		}

		if err = r.L.WaitN(ctx, size); err != nil {
			return
		}

		curr, err1 := r.R.Read(p[n : n+size])
		n += curr
		if n == l {
			return n, nil
		}

		if err1 != nil {
			return n, err1
		}
	}
}

// Close closes the underlying reader.
func (r *rateLimitReader) Close() error {
	return r.R.Close()
}
