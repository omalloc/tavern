package sharedkv

import (
	"errors"
	"testing"

	"github.com/omalloc/tavern/pkg/lifecycle"
)

func TestClosedSharedKVRejectsOldProcessQueries(t *testing.T) {
	kv := NewMemSharedKV()
	if err := kv.Set(t.Context(), []byte("key"), []byte("value")); err != nil {
		t.Fatal(err)
	}
	owned, err := kv.Get(t.Context(), []byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = kv.Close(); err != nil {
		t.Fatal(err)
	}
	if string(owned) != "value" {
		t.Fatal("value lifetime depended on open DB")
	}
	if _, err = kv.Get(t.Context(), []byte("key")); !errors.Is(err, lifecycle.ErrDraining) {
		t.Fatal(err)
	}
	if err = kv.Set(t.Context(), []byte("key"), owned); !errors.Is(err, lifecycle.ErrDraining) {
		t.Fatal(err)
	}
	if err = kv.IteratePrefix(t.Context(), []byte("key"), func([]byte, []byte) error { t.Fatal("closed iterator called back"); return nil }); !errors.Is(err, lifecycle.ErrDraining) {
		t.Fatal(err)
	}
}
