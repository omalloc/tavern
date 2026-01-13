package iobuf

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

type RateLimitedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
}

func NewRateLimitedWriter(w io.Writer, bytesPerSec int) *RateLimitedWriter {
	return &RateLimitedWriter{
		w:       w,
		limiter: rate.NewLimiter(rate.Limit(bytesPerSec), bytesPerSec),
	}
}

func (rw *RateLimitedWriter) Write(p []byte) (int, error) {
	// Wait until tokens are available
	if err := rw.limiter.WaitN(context.Background(), len(p)); err != nil {
		return 0, err
	}
	return rw.w.Write(p)
}

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
