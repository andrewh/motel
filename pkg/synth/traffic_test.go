// Tests for traffic pattern implementations
// Validates rate curves for all pattern types and composite overlays
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

	t.Run("bursty", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{Rate: "100/s", Pattern: "bursty"})
		require.NoError(t, err)
		assert.IsType(t, &BurstyPattern{}, p)

		bp := p.(*BurstyPattern)
		assert.InDelta(t, defaultBurstMultiplier, bp.BurstMultiplier, 0.001)
		assert.Equal(t, defaultBurstInterval, bp.BurstInterval)
		assert.Equal(t, defaultBurstDuration, bp.BurstDuration)
	})

	t.Run("bursty with custom parameters", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{
			Rate:            "100/s",
			Pattern:         "bursty",
			BurstMultiplier: 3,
			BurstInterval:   "2m",
			BurstDuration:   "15s",
		})
		require.NoError(t, err)
		bp := p.(*BurstyPattern)
		assert.InDelta(t, 3.0, bp.BurstMultiplier, 0.001)
		assert.Equal(t, 2*time.Minute, bp.BurstInterval)
		assert.Equal(t, 15*time.Second, bp.BurstDuration)
	})

	t.Run("bursty with partial custom parameters", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{
			Rate:            "100/s",
			Pattern:         "bursty",
			BurstMultiplier: 10,
		})
		require.NoError(t, err)
		bp := p.(*BurstyPattern)
		assert.InDelta(t, 10.0, bp.BurstMultiplier, 0.001)
		assert.Equal(t, defaultBurstInterval, bp.BurstInterval)
		assert.Equal(t, defaultBurstDuration, bp.BurstDuration)
	})

	t.Run("bursty with invalid burst_interval", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:          "100/s",
			Pattern:       "bursty",
			BurstInterval: "not-a-duration",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_interval")
	})

	t.Run("bursty with invalid burst_duration", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:          "100/s",
			Pattern:       "bursty",
			BurstDuration: "garbage",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_duration")
	})

	t.Run("custom pattern", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "custom",
			Segments: []SegmentConfig{
				{Until: "5m", Rate: "50/s"},
				{Until: "10m", Rate: "200/s"},
			},
		})
		require.NoError(t, err)
		assert.IsType(t, &customPattern{}, p)
	})

	t.Run("custom pattern with no segments", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "custom",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "segments")
	})

	t.Run("custom pattern with invalid segment until", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "custom",
			Segments: []SegmentConfig{
				{Until: "bad", Rate: "50/s"},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "until")
	})

	t.Run("custom pattern with invalid segment rate", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "custom",
			Segments: []SegmentConfig{
				{Until: "5m", Rate: "bad"},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rate")
	})

	t.Run("with overlay produces composite", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "diurnal",
			Overlay: &TrafficConfig{
				Rate:            "100/s",
				Pattern:         "bursty",
				BurstMultiplier: 3,
				BurstInterval:   "2m",
				BurstDuration:   "15s",
			},
		})
		require.NoError(t, err)
		assert.IsType(t, &compositePattern{}, p)
	})

	t.Run("overlay with invalid config", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "uniform",
			Overlay: &TrafficConfig{
				Rate:    "bad",
				Pattern: "bursty",
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overlay")
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

	t.Run("default parameters", func(t *testing.T) {
		t.Parallel()
		p := &DiurnalPattern{
			BaseRate:         100,
			PeakMultiplier:   defaultPeakMultiplier,
			TroughMultiplier: defaultTroughMultiplier,
			Period:           defaultDiurnalPeriod,
		}

		rates := make([]float64, 24)
		for i := range 24 {
			rates[i] = p.Rate(time.Duration(i) * time.Hour)
		}

		minRate, maxRate := rates[0], rates[0]
		for _, r := range rates[1:] {
			if r < minRate {
				minRate = r
			}
			if r > maxRate {
				maxRate = r
			}
		}

		assert.Greater(t, maxRate, minRate, "diurnal pattern should have variation")
		assert.Greater(t, minRate, 0.0, "rate should never be negative")
		assert.InDelta(t, 150.0, maxRate, 1.0)
		assert.InDelta(t, 50.0, minRate, 1.0)
	})

	t.Run("custom parameters", func(t *testing.T) {
		t.Parallel()
		p := &DiurnalPattern{
			BaseRate:         100,
			PeakMultiplier:   2.0,
			TroughMultiplier: 0.2,
			Period:           12 * time.Hour,
		}

		rates := make([]float64, 12)
		for i := range 12 {
			rates[i] = p.Rate(time.Duration(i) * time.Hour)
		}

		minRate, maxRate := rates[0], rates[0]
		for _, r := range rates[1:] {
			if r < minRate {
				minRate = r
			}
			if r > maxRate {
				maxRate = r
			}
		}

		assert.InDelta(t, 200.0, maxRate, 1.0)
		assert.InDelta(t, 20.0, minRate, 1.0)
	})

	t.Run("constructed via NewTrafficPattern with defaults", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{Rate: "100/s", Pattern: "diurnal"})
		require.NoError(t, err)
		dp := p.(*DiurnalPattern)
		assert.InDelta(t, defaultPeakMultiplier, dp.PeakMultiplier, 0.001)
		assert.InDelta(t, defaultTroughMultiplier, dp.TroughMultiplier, 0.001)
		assert.Equal(t, defaultDiurnalPeriod, dp.Period)
	})

	t.Run("constructed via NewTrafficPattern with custom values", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{
			Rate:             "100/s",
			Pattern:          "diurnal",
			PeakMultiplier:   2.0,
			TroughMultiplier: 0.2,
			Period:           "12h",
		})
		require.NoError(t, err)
		dp := p.(*DiurnalPattern)
		assert.InDelta(t, 2.0, dp.PeakMultiplier, 0.001)
		assert.InDelta(t, 0.2, dp.TroughMultiplier, 0.001)
		assert.Equal(t, 12*time.Hour, dp.Period)
	})

	t.Run("invalid period", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "diurnal",
			Period:  "bad",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "period")
	})
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

func TestCustomPattern(t *testing.T) {
	t.Parallel()

	t.Run("walks segments in order", func(t *testing.T) {
		t.Parallel()
		p := &customPattern{
			BaseRate: 100,
			Segments: []segment{
				{Until: 5 * time.Minute, Rate: 50},
				{Until: 10 * time.Minute, Rate: 200},
			},
		}

		assert.InDelta(t, 50.0, p.Rate(0), 0.001)
		assert.InDelta(t, 50.0, p.Rate(3*time.Minute), 0.001)
		assert.InDelta(t, 200.0, p.Rate(5*time.Minute), 0.001)
		assert.InDelta(t, 200.0, p.Rate(9*time.Minute), 0.001)
	})

	t.Run("falls back to base rate after all segments", func(t *testing.T) {
		t.Parallel()
		p := &customPattern{
			BaseRate: 100,
			Segments: []segment{
				{Until: 5 * time.Minute, Rate: 50},
			},
		}

		assert.InDelta(t, 50.0, p.Rate(3*time.Minute), 0.001)
		assert.InDelta(t, 100.0, p.Rate(5*time.Minute), 0.001)
		assert.InDelta(t, 100.0, p.Rate(10*time.Minute), 0.001)
	})

	t.Run("single segment", func(t *testing.T) {
		t.Parallel()
		p := &customPattern{
			BaseRate: 100,
			Segments: []segment{
				{Until: time.Hour, Rate: 500},
			},
		}

		assert.InDelta(t, 500.0, p.Rate(0), 0.001)
		assert.InDelta(t, 500.0, p.Rate(30*time.Minute), 0.001)
		assert.InDelta(t, 100.0, p.Rate(time.Hour), 0.001)
	})

	t.Run("unsorted segments are sorted by constructor", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "custom",
			Segments: []SegmentConfig{
				{Until: "10m", Rate: "200/s"},
				{Until: "5m", Rate: "50/s"},
			},
		})
		require.NoError(t, err)

		cp := p.(*customPattern)
		assert.InDelta(t, 50.0, cp.Rate(3*time.Minute), 0.001)
		assert.InDelta(t, 200.0, cp.Rate(7*time.Minute), 0.001)
		assert.InDelta(t, 100.0, cp.Rate(15*time.Minute), 0.001)
	})
}

func TestCompositePattern(t *testing.T) {
	t.Parallel()

	t.Run("overlay modulates base rate", func(t *testing.T) {
		t.Parallel()
		base := &UniformPattern{BaseRate: 100}
		overlay := &BurstyPattern{
			BaseRate:        100,
			BurstMultiplier: 5,
			BurstInterval:   5 * time.Minute,
			BurstDuration:   30 * time.Second,
		}
		cp := &compositePattern{Base: base, Overlay: overlay, OverlayBaseRate: 100}

		// During burst (start of cycle), overlay rate = 500, factor = 500/100 = 5
		assert.InDelta(t, 500.0, cp.Rate(0), 0.001)

		// During normal period (1 min into cycle), overlay rate = 100, factor = 1
		assert.InDelta(t, 100.0, cp.Rate(1*time.Minute), 0.001)
	})

	t.Run("diurnal base with bursty overlay", func(t *testing.T) {
		t.Parallel()
		base := &DiurnalPattern{
			BaseRate:         100,
			PeakMultiplier:   defaultPeakMultiplier,
			TroughMultiplier: defaultTroughMultiplier,
			Period:           defaultDiurnalPeriod,
		}
		overlay := &BurstyPattern{
			BaseRate:        100,
			BurstMultiplier: 3,
			BurstInterval:   10 * time.Minute,
			BurstDuration:   1 * time.Minute,
		}
		cp := &compositePattern{Base: base, Overlay: overlay, OverlayBaseRate: 100}

		// At 2 minutes (normal overlay period), composite = base diurnal rate * 1.0
		baseAt2m := base.Rate(2 * time.Minute)
		assert.InDelta(t, baseAt2m, cp.Rate(2*time.Minute), 0.001)

		// At 10 minutes (burst start), composite = base diurnal rate * 3.0
		baseAt10m := base.Rate(10 * time.Minute)
		assert.InDelta(t, baseAt10m*3.0, cp.Rate(10*time.Minute), 0.001)
	})

	t.Run("zero overlay base rate returns base rate", func(t *testing.T) {
		t.Parallel()
		base := &UniformPattern{BaseRate: 100}
		overlay := &UniformPattern{BaseRate: 50}
		cp := &compositePattern{Base: base, Overlay: overlay, OverlayBaseRate: 0}

		assert.InDelta(t, 100.0, cp.Rate(0), 0.001)
	})
}

func TestBurstyPatternValidation(t *testing.T) {
	t.Parallel()

	t.Run("zero burst interval rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:          "100/s",
			Pattern:       "bursty",
			BurstInterval: "0s",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_interval")
	})

	t.Run("zero burst duration rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:          "100/s",
			Pattern:       "bursty",
			BurstDuration: "0s",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_duration")
	})

	t.Run("burst duration equals burst interval rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:          "100/s",
			Pattern:       "bursty",
			BurstInterval: "5m",
			BurstDuration: "5m",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_duration")
	})

	t.Run("burst duration exceeds burst interval rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:          "100/s",
			Pattern:       "bursty",
			BurstInterval: "1m",
			BurstDuration: "2m",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_duration")
	})

	t.Run("negative burst multiplier rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:            "100/s",
			Pattern:         "bursty",
			BurstMultiplier: -5,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_multiplier")
	})

	t.Run("zero burst multiplier uses default", func(t *testing.T) {
		t.Parallel()
		p, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "bursty",
		})
		require.NoError(t, err)
		bp := p.(*BurstyPattern)
		assert.InDelta(t, defaultBurstMultiplier, bp.BurstMultiplier, 0.001)
	})
}

func TestDiurnalPatternValidation(t *testing.T) {
	t.Parallel()

	t.Run("zero period rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "diurnal",
			Period:  "0s",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "period")
	})

	t.Run("negative peak multiplier rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:           "100/s",
			Pattern:        "diurnal",
			PeakMultiplier: -2,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "peak_multiplier")
	})

	t.Run("negative trough multiplier rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:             "100/s",
			Pattern:          "diurnal",
			TroughMultiplier: -1,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "trough_multiplier")
	})

	t.Run("peak less than trough rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:             "100/s",
			Pattern:          "diurnal",
			PeakMultiplier:   0.5,
			TroughMultiplier: 1.5,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "peak_multiplier")
	})

	t.Run("zero trough multiplier accepted", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:             "100/s",
			Pattern:          "diurnal",
			PeakMultiplier:   2.0,
			TroughMultiplier: 0,
		})
		require.NoError(t, err)
	})
}

func TestCustomPatternValidation(t *testing.T) {
	t.Parallel()

	t.Run("duplicate segment until values rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewTrafficPattern(TrafficConfig{
			Rate:    "100/s",
			Pattern: "custom",
			Segments: []SegmentConfig{
				{Until: "5m", Rate: "50/s"},
				{Until: "5m", Rate: "200/s"},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate")
	})
}
