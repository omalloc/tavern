package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
)

type handoffProbe struct {
	storagev1.Storage
	events  []string
	failure string
}

func (p *handoffProbe) BeginCacheTask(context.Context, time.Duration) (context.Context, func(), error) {
	panic("not needed")
}
func (p *handoffProbe) Drain(context.Context) error {
	p.events = append(p.events, "drain")
	if p.failure == "drain" {
		return errors.New("injected drain failure")
	}
	return nil
}
func (p *handoffProbe) Close() error {
	p.events = append(p.events, "close")
	if p.failure == "close" {
		return errors.New("injected close failure")
	}
	return nil
}
func TestHandoffRequiresDrainAndCloseBeforeStartingWriter(t *testing.T) {
	for _, failure := range []string{"", "drain", "close", "upgrade"} {
		t.Run(failure, func(t *testing.T) {
			p := &handoffProbe{failure: failure}
			err := handoffStorage(t.Context(), p, func() error {
				p.events = append(p.events, "upgrade")
				if failure == "upgrade" {
					return errors.New("injected upgrade failure")
				}
				return nil
			})
			want := []string{"drain", "close", "upgrade"}
			if failure == "drain" {
				want = want[:1]
			}
			if failure == "close" {
				want = want[:2]
			}
			if !reflect.DeepEqual(p.events, want) || (err != nil) != (failure != "") {
				t.Fatal(p.events, err)
			}
		})
	}
}
