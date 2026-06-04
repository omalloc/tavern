package metrics

import (
	"math"
	"sync"
	"testing"
)

func TestCounterSmoother_FirstCallReturnsZero(t *testing.T) {
	cs := NewCounterSmoother(0.3)
	got := cs.Update(100)
	if got != 0 {
		t.Fatalf("first call must return 0, got %f", got)
	}
}

func TestCounterSmoother_Increasing(t *testing.T) {
	cs := NewCounterSmoother(1.0) // no smoothing
	cs.Update(100)
	got := cs.Update(110)
	if got != 10 {
		t.Fatalf("expected raw delta 10 with alpha=1.0, got %f", got)
	}
}

func TestCounterSmoother_NoGrowthDecays(t *testing.T) {
	cs := NewCounterSmoother(0.5)
	cs.Update(100)
	cs.Update(110) // builds up smoothed
	got := cs.Update(110) // no growth
	if got >= 10 {
		t.Fatalf("no-growth must decay below last delta, got %f", got)
	}
}

func TestCounterSmoother_ResetHandling(t *testing.T) {
	cs := NewCounterSmoother(0.5)
	cs.Update(100)
	cs.Update(120) // delta=20
	got := cs.Update(0) // counter reset, raw=0, delta=-120
	if got > 20 {
		t.Fatalf("reset must decay, got %f", got)
	}
	if math.IsNaN(got) {
		t.Fatal("result is NaN")
	}
}

func TestCounterSmoother_Smoothing(t *testing.T) {
	cs := NewCounterSmoother(0.3)
	cs.Update(0)
	first := cs.Update(10) // delta=10, smoothed=0.3*10+0.7*0=3
	if math.Abs(first-3.0) > 1e-9 {
		t.Fatalf("expected ~3, got %f", first)
	}
	second := cs.Update(20) // delta=10, smoothed=0.3*10+0.7*3=5.1
	if math.Abs(second-5.1) > 1e-9 {
		t.Fatalf("expected ~5.1, got %f", second)
	}
}

func TestCounterSmoother_Reset(t *testing.T) {
	cs := NewCounterSmoother(0.3)
	cs.Update(100)
	cs.Update(110) // smoothed = 3

	cs.Reset()
	// After Reset, first call should return 0 (new baseline)
	got := cs.Update(200)
	if got != 0 {
		t.Fatalf("after Reset, first call must return 0, got %f", got)
	}
	// Second call: delta = 210 - 200 = 10, smoothed = 0.3*10 + 0.7*0 = 3
	got = cs.Update(210)
	if math.Abs(got-3.0) > 1e-9 {
		t.Fatalf("after Reset baseline, expected ~3, got %f", got)
	}
}

func TestCounterSmoother_NaNInputIgnored(t *testing.T) {
	cs := NewCounterSmoother(0.5)
	cs.Update(100)
	cs.Update(110) // smoothed = 5

	// NaN input must not corrupt state
	got := cs.Update(math.NaN())
	if math.IsNaN(got) {
		t.Fatal("NaN input must not return NaN")
	}
	if got != 5.0 {
		t.Fatalf("NaN input must return current smoothed (5.0), got %f", got)
	}

	// Subsequent normal call must still work
	got = cs.Update(120) // delta=10, smoothed=0.5*10+0.5*5=7.5
	if math.Abs(got-7.5) > 1e-9 {
		t.Fatalf("after NaN, expected ~7.5, got %f", got)
	}
}

func TestCounterSmoother_InfInputIgnored(t *testing.T) {
	cs := NewCounterSmoother(0.5)
	cs.Update(100)
	cs.Update(110) // smoothed = 5

	got := cs.Update(math.Inf(1))
	if got != 5.0 {
		t.Fatalf("Inf input must return current smoothed (5.0), got %f", got)
	}

	// State preserved; next normal call works
	got = cs.Update(120)
	if math.Abs(got-7.5) > 1e-9 {
		t.Fatalf("after Inf, expected ~7.5, got %f", got)
	}
}

func TestNewCounterSmoother_AlphaClamped(t *testing.T) {
	tests := []struct {
		input, want float64
	}{
		{-1.0, 0},
		{-0.5, 0},
		{0.0, 0},
		{0.3, 0.3},
		{1.0, 1.0},
		{1.5, 1.0},
		{99.0, 1.0},
	}
	for _, tc := range tests {
		cs := NewCounterSmoother(tc.input)
		if cs.Alpha != tc.want {
			t.Errorf("NewCounterSmoother(%v): Alpha = %v, want %v", tc.input, cs.Alpha, tc.want)
		}
	}
}

func TestCounterSmoother_Concurrent(t *testing.T) {
	cs := NewCounterSmoother(0.3)
	cs.Update(0) // init

	var wg sync.WaitGroup
	n := 100
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(v float64) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = cs.Update(v)
			}
		}(float64(i * 100))
	}
	wg.Wait()
	// No data race detected under -race; result is non-NaN
	if math.IsNaN(cs.Update(99999)) {
		t.Fatal("final value is NaN after concurrent updates")
	}
}
