package pebble

import (
	"context"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/omalloc/tavern/contrib/log"
)

const (
	writebackInterval = time.Second
	writebackMaxKeys  = 1024
	writebackMaxBytes = 4 << 20
)

// The overlay makes accepted writes immediately visible, including to scans.
// Only the newest mutation of each key is retained. All DB mutations go through
// this mutex: a synchronous barrier atomically drains older pending mutations
// with the barrier, so a later background batch can never replay stale state.
type writebackQueue struct {
	mu      sync.Mutex
	pending map[string][]byte // nil values are deletions
	bytes   int
	dirty   bool
	err     error
	stop    chan struct{}
	done    chan struct{}
}

func (p *PebbleDB) startWriteback() {
	p.writeback.stop, p.writeback.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(p.writeback.done)
		ticker := time.NewTicker(writebackInterval)
		defer ticker.Stop()
		for {
			select {
			case <-p.writeback.stop:
				return
			case <-ticker.C:
				if err := p.flushWriteback(); err != nil {
					log.NewHelper(log.GetLogger()).Errorf("pebble cache writeback failed: %v", err)
				}
			}
		}
	}()
}

func (p *PebbleDB) write(ctx context.Context, key, value []byte, syncWrite bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q := &p.writeback
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !syncWrite && q.err != nil {
		return q.err
	}
	if q.pending == nil {
		q.pending = make(map[string][]byte)
	}
	name := string(key)
	if old, exists := q.pending[name]; exists {
		q.bytes -= len(name) + len(old)
	}
	q.pending[name] = value
	q.bytes += len(name) + len(value)
	q.dirty = true
	if syncWrite || len(q.pending) >= writebackMaxKeys || q.bytes >= writebackMaxBytes {
		options := pebble.NoSync
		if syncWrite {
			options = pebble.Sync
		}
		q.err = p.flushPendingLocked(options)
		return q.err
	}
	return nil
}

// Caller holds the overlay mutex. Failed batches remain queued for retry.
func (p *PebbleDB) flushPendingLocked(options *pebble.WriteOptions) error {
	q := &p.writeback
	if len(q.pending) == 0 {
		return nil
	}
	batch := p.db.NewBatch()
	defer batch.Close()
	for key, value := range q.pending {
		var err error
		if value == nil {
			err = batch.Delete([]byte(key), nil)
		} else {
			err = batch.Set([]byte(key), value, nil)
		}
		if err != nil {
			return err
		}
	}
	if err := batch.Commit(options); err != nil {
		return err
	}
	clear(q.pending)
	q.bytes = 0
	return nil
}

func (p *PebbleDB) flushWriteback() error {
	q := &p.writeback
	q.mu.Lock()
	if err := p.flushPendingLocked(pebble.NoSync); err != nil {
		q.err = err
		q.mu.Unlock()
		return err
	}
	dirty := q.dirty
	q.dirty = false
	q.mu.Unlock()
	if !dirty {
		return nil
	}
	// Sync one WAL marker for the whole interval outside the overlay lock.
	// Ordinary requests can continue accepting writes while this I/O runs.
	err := p.db.LogData(nil, pebble.Sync)
	q.mu.Lock()
	q.err = err
	if err != nil {
		q.dirty = true
	}
	q.mu.Unlock()
	return err
}

// Creating the indexed iterator while holding the overlay lock captures the
// committed sequence and pending overlay together. Callbacks run after unlock.
func (p *PebbleDB) readIterator() (*pebble.Iterator, func(), error) {
	q := &p.writeback
	q.mu.Lock()
	defer q.mu.Unlock()
	batch := p.db.NewIndexedBatch()
	for key, value := range q.pending {
		var err error
		if value == nil {
			err = batch.Delete([]byte(key), nil)
		} else {
			err = batch.Set([]byte(key), value, nil)
		}
		if err != nil {
			batch.Close()
			return nil, nil, err
		}
	}
	iter, err := batch.NewIter(nil)
	if err != nil {
		batch.Close()
		return nil, nil, err
	}
	return iter, func() { iter.Close(); batch.Close() }, nil
}
