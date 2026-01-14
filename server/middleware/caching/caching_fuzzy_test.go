package caching

import (
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

func TestCalculateSoftTTL(t *testing.T) {
	tests := []struct {
		name        string
		respUnix    int64
		expiresAt   int64
		fuzzyRate   float64
		wantSoftTTL int64
	}{
		{
			name:        "standard case - 80% of 600s",
			respUnix:    1000,
			expiresAt:   1600,
			fuzzyRate:   0.8,
			wantSoftTTL: 1480, // 1000 + (600 * 0.8) = 1480
		},
		{
			name:        "standard case - 90% of 3600s",
			respUnix:    1000,
			expiresAt:   4600,
			fuzzyRate:   0.9,
			wantSoftTTL: 4240, // 1000 + (3600 * 0.9) = 4240
		},
		{
			name:        "invalid rate - should default to 0.8",
			respUnix:    1000,
			expiresAt:   1600,
			fuzzyRate:   1.5,
			wantSoftTTL: 1480, // 1000 + (600 * 0.8) = 1480
		},
		{
			name:        "zero rate - should default to 0.8",
			respUnix:    1000,
			expiresAt:   1600,
			fuzzyRate:   0,
			wantSoftTTL: 1480, // 1000 + (600 * 0.8) = 1480
		},
		{
			name:        "already expired",
			respUnix:    1000,
			expiresAt:   900,
			fuzzyRate:   0.8,
			wantSoftTTL: 900, // Should return expiresAt when already expired
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateSoftTTL(tt.respUnix, tt.expiresAt, tt.fuzzyRate)
			if got != tt.wantSoftTTL {
				t.Errorf("calculateSoftTTL() = %v, want %v", got, tt.wantSoftTTL)
			}
		})
	}
}

func TestShouldTriggerFuzzyRefresh(t *testing.T) {
	softTTL := int64(1000)
	hardTTL := int64(1100)

	tests := []struct {
		name        string
		now         int64
		description string
	}{
		{
			name:        "before soft TTL",
			now:         900,
			description: "should never trigger before soft TTL",
		},
		{
			name:        "at soft TTL",
			now:         1000,
			description: "should have 0% probability at soft TTL",
		},
		{
			name:        "midpoint",
			now:         1050,
			description: "should have 50% probability at midpoint",
		},
		{
			name:        "near hard TTL",
			now:         1099,
			description: "should have ~99% probability near hard TTL",
		},
		{
			name:        "at hard TTL",
			now:         1100,
			description: "should never trigger at hard TTL (handled by hasExpired)",
		},
		{
			name:        "after hard TTL",
			now:         1200,
			description: "should never trigger after hard TTL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run multiple times to check probability behavior
			iterations := 100
			triggered := 0

			for i := 0; i < iterations; i++ {
				if shouldTriggerFuzzyRefresh(tt.now, softTTL, hardTTL) {
					triggered++
				}
			}

			// Check boundary conditions
			if tt.now < softTTL || tt.now >= hardTTL {
				if triggered > 0 {
					t.Errorf("shouldTriggerFuzzyRefresh() triggered %d times out of %d, should never trigger %s",
						triggered, iterations, tt.description)
				}
			} else {
				// In the fuzzy zone, we expect some triggers based on probability
				t.Logf("Triggered %d times out of %d at now=%d (soft=%d, hard=%d) - %s",
					triggered, iterations, tt.now, softTTL, hardTTL, tt.description)
			}
		})
	}
}

func TestShouldTriggerFuzzyRefreshProbability(t *testing.T) {
	softTTL := int64(1000)
	hardTTL := int64(2000)
	iterations := 1000

	tests := []struct {
		name              string
		now               int64
		expectedProbRange [2]float64 // min and max expected probability
	}{
		{
			name:              "at soft TTL (0% probability)",
			now:               1000,
			expectedProbRange: [2]float64{0, 0.05}, // Allow small margin
		},
		{
			name:              "25% into fuzzy zone",
			now:               1250,
			expectedProbRange: [2]float64{0.15, 0.35},
		},
		{
			name:              "50% into fuzzy zone",
			now:               1500,
			expectedProbRange: [2]float64{0.40, 0.60},
		},
		{
			name:              "75% into fuzzy zone",
			now:               1750,
			expectedProbRange: [2]float64{0.65, 0.85},
		},
		{
			name:              "95% into fuzzy zone",
			now:               1950,
			expectedProbRange: [2]float64{0.90, 1.0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			triggered := 0
			for i := 0; i < iterations; i++ {
				if shouldTriggerFuzzyRefresh(tt.now, softTTL, hardTTL) {
					triggered++
				}
			}

			probability := float64(triggered) / float64(iterations)
			t.Logf("Probability at now=%d: %.2f (triggered %d/%d)",
				tt.now, probability, triggered, iterations)

			if probability < tt.expectedProbRange[0] || probability > tt.expectedProbRange[1] {
				t.Errorf("Probability %.2f outside expected range [%.2f, %.2f]",
					probability, tt.expectedProbRange[0], tt.expectedProbRange[1])
			}
		})
	}
}

func TestHasExpired(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		expiresAt int64
		want      bool
	}{
		{
			name:      "not expired - 1 hour in future",
			expiresAt: now.Add(1 * time.Hour).Unix(),
			want:      false,
		},
		{
			name:      "expired - 1 hour in past",
			expiresAt: now.Add(-1 * time.Hour).Unix(),
			want:      true,
		},
		{
			name:      "just expired",
			expiresAt: now.Add(-1 * time.Second).Unix(),
			want:      true,
		},
		{
			name:      "not expired - just created",
			expiresAt: now.Add(1 * time.Second).Unix(),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := &object.Metadata{
				ExpiresAt: tt.expiresAt,
			}
			got := hasExpired(md)
			if got != tt.want {
				t.Errorf("hasExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
