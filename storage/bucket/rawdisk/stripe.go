package rawdisk

import (
	"errors"
	"sync"
)

var (
	ErrNotEnoughSpace = errors.New("not enough space in stripe")
)

// StripeManager manages the ring buffer of the raw disk.
// It keeps track of the current write cursor and handles wrap-around logic.
type StripeManager struct {
	mu           sync.Mutex
	stripeOffset uint64
	stripeSize   uint64
	cursor       uint64 // current write offset within the stripe area (relative to 0)
}

// NewStripeManager creates a new StripeManager for a given stripe area.
// offset: absolute offset on disk where the stripe area begins.
// size: total size of the stripe area.
// cursor: last recorded relative cursor position.
func NewStripeManager(offset, size, cursor uint64) *StripeManager {
	return &StripeManager{
		stripeOffset: offset,
		stripeSize:   size,
		cursor:       cursor,
	}
}

// Allocate space for writing.
// Returns the absolute offset on disk and a boolean indicating if it wrapped around.
// size must be aligned (e.g., 4096 bytes).
func (sm *StripeManager) Allocate(size uint32) (uint64, bool, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	size64 := uint64(size)
	if size64 > sm.stripeSize {
		return 0, false, ErrNotEnoughSpace
	}

	wrapped := false
	if sm.cursor+size64 > sm.stripeSize {
		// Wrap around to the beginning of the stripe area
		sm.cursor = 0
		wrapped = true
	}

	absoluteOffset := sm.stripeOffset + sm.cursor
	sm.cursor += size64

	return absoluteOffset, wrapped, nil
}

// Cursor returns the current relative write cursor.
func (sm *StripeManager) Cursor() uint64 {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.cursor
}
