package metrics

import (
	"math"
	"sync"
)

// CounterSmoother computes an exponentially-weighted moving average (EWMA) of
// the per-tick delta of a monotonically-increasing counter.
//
// It is safe for concurrent use.
//
// Alpha controls the smoothing factor: Alpha=0 means the smoothed value never
// changes (frozen at the first non-zero delta), Alpha=1 means no smoothing at
// all (raw delta returned as-is). Typical values for QPS smoothing are 0.2–0.5.
type CounterSmoother struct {
	mu       sync.Mutex
	lastRaw  float64
	smoothed float64
	Alpha    float64
	init     bool
}

// NewCounterSmoother returns an initialized CounterSmoother. alpha is clamped
// to [0, 1].
func NewCounterSmoother(alpha float64) *CounterSmoother {
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}
	return &CounterSmoother{Alpha: alpha}
}

// Update receives the current raw cumulative counter value and returns the
// smoothed per-tick delta.
//
// On the first call it records the baseline and returns 0. When the raw value
// does not increase (delta <= 0) the smoothed value decays exponentially
// towards zero — this handles counter resets and idle periods gracefully.
//
// NaN and Inf inputs are silently ignored (the current smoothed value is
// returned unchanged and internal state is not corrupted).
func (s *CounterSmoother) Update(raw float64) float64 {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		s.mu.Lock()
		v := s.smoothed
		s.mu.Unlock()
		return v
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.init {
		s.lastRaw = raw
		s.init = true
		return 0
	}

	delta := raw - s.lastRaw
	s.lastRaw = raw

	if !(delta > 0) {
		// Counter didn't grow (delta <= 0, including NaN from Inf-Inf);
		// exponential decay towards zero.
		s.smoothed = (1 - s.Alpha) * s.smoothed
		return s.smoothed
	}

	s.smoothed = s.Alpha*delta + (1-s.Alpha)*s.smoothed
	return s.smoothed
}

// Reset discards all internal state so the next call to Update behaves as if
// the smoother were newly created. Alpha is preserved.
func (s *CounterSmoother) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRaw = 0
	s.smoothed = 0
	s.init = false
}
