package iobuf

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

type rateLimitReader struct {
	R io.ReadCloser
	L *rate.Limiter
}

func NewRateLimitReader(r io.ReadCloser, Kbps int) io.ReadCloser {
	l := Kbps << 10
	return &rateLimitReader{
		R: r,
		L: rate.NewLimiter(rate.Limit(l), l),
	}
}

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

func (r *rateLimitReader) Close() error {
	return r.R.Close()
}
