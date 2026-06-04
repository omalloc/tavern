package iobuf

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

// RateLimitedWriter throttles writes through a token-bucket limiter.
// Each Write call waits for enough tokens before writing to the underlying writer.
type RateLimitedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
}

// NewRateLimitedWriter returns a *RateLimitedWriter that limits write throughput to
// bytesPerSec bytes per second. The burst is set equal to one second of tokens.
func NewRateLimitedWriter(w io.Writer, bytesPerSec int) *RateLimitedWriter {
	return &RateLimitedWriter{
		w:       w,
		limiter: rate.NewLimiter(rate.Limit(bytesPerSec), bytesPerSec),
	}
}

// Write waits for tokens for len(p) bytes, then delegates to the underlying writer.
func (rw *RateLimitedWriter) Write(p []byte) (int, error) {
	if err := rw.limiter.WaitN(context.Background(), len(p)); err != nil {
		return 0, err
	}
	return rw.w.Write(p)
}

// CopyWithRateLimit copies from src to dst using a 32 KB buffer, waiting for
// tokens before each write. Returns nil on io.EOF from src.
//
// Unlike RateLimitedWriter which wraps a single writer, this function can be used
// with any io.Reader/io.Writer pair and a shared limiter.
func CopyWithRateLimit(dst io.Writer, src io.Reader, limiter *rate.Limiter) error {
	buf := make([]byte, 32*1024)

	for {
		n, err := src.Read(buf)
		if n > 0 {
			if err := limiter.WaitN(context.Background(), n); err != nil {
				return err
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
