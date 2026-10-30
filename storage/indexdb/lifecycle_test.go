package indexdb

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/lifecycle"
)

type closeProbeDB struct {
	storage.IndexDB
	closed atomic.Bool
	late   atomic.Bool
}

func (d *closeProbeDB) Close() error { d.closed.Store(true); return nil }
func (d *closeProbeDB) Set(context.Context, []byte, *object.Metadata) error {
	if d.closed.Load() {
		d.late.Store(true)
	}
	return nil
}
func (d *closeProbeDB) Iterate(_ context.Context, _ []byte, fn storage.IterateFunc) error {
	fn([]byte("key"), nil)
	return nil
}
func TestGuardedDBWaitsForCallbacksAndRejectsLateOperations(t *testing.T) {
	raw := &closeProbeDB{}
	db := &guardedDB{IndexDB: raw}
	entered, release, iterDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		db.Iterate(t.Context(), nil, func([]byte, *object.Metadata) bool { close(entered); <-release; return true })
		close(iterDone)
	}()
	<-entered
	closeDone := make(chan struct{})
	go func() { db.Close(); close(closeDone) }()
	deadline := time.After(time.Second)
	for {
		err := db.Set(t.Context(), nil, nil)
		if errors.Is(err, lifecycle.ErrDraining) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("shutdown did not close admission")
		case <-time.After(time.Millisecond):
		}
	}
	if raw.closed.Load() {
		t.Fatal("backend closed under active iterator")
	}
	close(release)
	<-iterDone
	<-closeDone
	if err := db.Set(t.Context(), nil, nil); !errors.Is(err, lifecycle.ErrDraining) {
		t.Fatal(err)
	}
	if raw.late.Load() {
		t.Fatal("late callback reached closed backend")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
