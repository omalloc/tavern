package iobuf_test

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/omalloc/tavern/pkg/iobuf"
)

func TestSavepartCloseConfirmsFullLimitedBlockWithoutInventingEOF(t *testing.T) {
	for _, async := range []bool{false, true} {
		for _, size := range []int64{3, 4} {
			t.Run(fmt.Sprintf("async=%t/bytes=%d", async, size), func(t *testing.T) {
				var saved []byte
				var eof bool
				var failure error
				onWrite := func(data []byte, _ uint32, _ uint64, end bool) error {
					saved = append(saved, data...)
					if end {
						t.Error("limiter invented source EOF")
					}
					return nil
				}
				onError := func(err error) { failure = err }
				onClose := func(end bool) { eof = end }
				source := io.NopCloser(bytes.NewReader([]byte("abcdefgh")))
				var body io.ReadCloser
				if async {
					body = iobuf.SavepartAsyncReader(source, 4, 0, onWrite, onError, onClose, 2)
				} else {
					body = iobuf.SavepartReader(source, 4, 0, onWrite, onError, onClose)
				}
				limited := iobuf.LimitReadCloser(body, size)
				data, err := io.ReadAll(limited)
				if err != nil || int64(len(data)) != size {
					t.Fatal(string(data), err)
				}
				if err = limited.Close(); err != nil {
					t.Fatal(err)
				}
				if failure != nil || eof {
					t.Fatal("limited close changed stream termination", failure, eof)
				}
				if size == 4 && string(saved) != "abcd" || size == 3 && len(saved) != 0 {
					t.Fatal("only complete blocks may survive a limited close", string(saved))
				}
			})
		}
	}
}
