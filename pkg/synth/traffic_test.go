// Tests for traffic pattern implementations
// Validates rate curves for uniform, diurnal, poisson, and bursty patterns
package synth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTrafficPattern(t *testing.T) {
	t.Parallel()

	t.Run("default is uniform", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{Rate: "100/s"})
		require.NoError(t, err)
		assert.IsType(t, &UniformPattern{}, p)
	})

	t.Run("explicit uniform", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{Rate: "50/s", Pattern: "uniform"})
		require.NoError(t, err)
		assert.IsType(t, &UniformPattern{}, p)
	})

	t.Run("diurnal", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{Rate: "100/s", Pattern: "diurnal"})
		require.NoError(t, err)
		assert.IsType(t, &DiurnalPattern{}, p)
	})

	t.Run("poisson", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{Rate: "100/s", Pattern: "poisson"})
		require.NoError(t, err)
		assert.IsType(t, &PoissonPattern{}, p)
	})

	t.Run("bursty", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{Rate: "100/s", Pattern: "bursty"})
		require.NoError(t, err)
		assert.IsType(t, &BurstyPattern{}, p)
	})

	t.Run("unknown pattern", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{Rate: "100/s", Pattern: "unknown"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown")
	})

	t.Run("invalid rate", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{Rate: "bad"})
		require.Error(t, err)
	})
}

func TestUniformPattern(t *testing.T) {
	t.Parallel()
	p := &UniformPattern{BaseRate: 100}
	assert.InDelta(t, 100.0, p.Rate(0), 0.001)
	assert.InDelta(t, 100.0, p.Rate(5*time.Minute), 0.001)
	assert.InDelta(t, 100.0, p.Rate(time.Hour), 0.001)
}

func TestDiurnalPattern(t *testing.T) {
	t.Parallel()
	p := &DiurnalPattern{BaseRate: 100}

	// Rate should vary over a 24-hour period
	rates := make([]float64, 24)
	for i := range 24 {
		rates[i] = p.Rate(time.Duration(i) * time.Hour)
	}

	// Find min and max
	minRate, maxRate := rates[0], rates[0]
	for _, r := range rates[1:] {
		if r < minRate {
			minRate = r
		}
		if r > maxRate {
			maxRate = r
		}
	}

	// Diurnal should have meaningful variation
	assert.Greater(t, maxRate, minRate, "diurnal pattern should have variation")
	assert.Greater(t, minRate, 0.0, "rate should never be negative")
}

func TestPoissonPattern(t *testing.T) {
	t.Parallel()
	p := &PoissonPattern{BaseRate: 100}

	// Rate should always equal the base rate (Poisson arrival times vary, not the rate itself)
	assert.InDelta(t, 100.0, p.Rate(0), 0.001)
	assert.InDelta(t, 100.0, p.Rate(5*time.Minute), 0.001)
}

func TestBurstyPattern(t *testing.T) {
	t.Parallel()
	p := &BurstyPattern{BaseRate: 100, BurstMultiplier: 5, BurstInterval: 5 * time.Minute, BurstDuration: 30 * time.Second}

	// During normal period, rate should be base rate
	normalRate := p.Rate(1 * time.Minute)
	assert.InDelta(t, 100.0, normalRate, 0.001)

	// During burst, rate should be higher
	burstRate := p.Rate(5 * time.Minute)
	assert.Greater(t, burstRate, normalRate)
}
