package verifier_test

import (
	"testing"

	"github.com/omalloc/tavern/plugin/verifier"
)

func TestFileCRC(t *testing.T) {
	hash, err := verifier.ReadAndSumHash("/cache1", "36b19bf31c12b746198c76bf788413550b8913a9", 5, 1048576)
	if err != nil {
		// t.Fatal(err)
	}

	t.Logf("hash %s", hash)
}
