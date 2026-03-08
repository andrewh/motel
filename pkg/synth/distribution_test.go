// Tests for duration distribution parsing and sampling
// Validates the "30ms +/- 10ms" DSL format and normal distribution sampling
package synth

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDistribution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		mean    time.Duration
		stddev  time.Duration
		wantErr string
	}{
		{
			name:   "milliseconds with +/-",
			input:  "30ms +/- 10ms",
			mean:   30 * time.Millisecond,
			stddev: 10 * time.Millisecond,
		},
		{
			name:   "milliseconds with unicode ±",
			input:  "30ms ± 10ms",
			mean:   30 * time.Millisecond,
			stddev: 10 * time.Millisecond,
		},
		{
			name:   "seconds",
			input:  "2s +/- 500ms",
			mean:   2 * time.Second,
			stddev: 500 * time.Millisecond,
		},
		{
			name:   "microseconds",
			input:  "100us +/- 20us",
			mean:   100 * time.Microsecond,
			stddev: 20 * time.Microsecond,
		},
		{
			name:   "fractional milliseconds",
			input:  "1ms +/- 0.5ms",
			mean:   1 * time.Millisecond,
			stddev: 500 * time.Microsecond,
		},
		{
			name:   "fixed duration no variance",
			input:  "50ms",
			mean:   50 * time.Millisecond,
			stddev: 0,
		},
		{
			name:   "extra whitespace",
			input:  "  30ms  +/-  10ms  ",
			mean:   30 * time.Millisecond,
			stddev: 10 * time.Millisecond,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: "duration is required",
		},
		{
			name:    "invalid mean",
			input:   "abc +/- 10ms",
			wantErr: "invalid mean duration",
		},
		{
			name:    "invalid stddev",
			input:   "30ms +/- xyz",
			wantErr: "invalid stddev duration",
		},
		{
			name:    "negative mean",
			input:   "-5ms +/- 1ms",
			wantErr: "mean duration must be positive",
		},
		{
			name:    "negative stddev",
			input:   "30ms +/- -5ms",
			wantErr: "stddev must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d, err := ParseDistribution(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.mean, d.Mean)
			assert.Equal(t, tt.stddev, d.StdDev)
		})
	}
}

func TestDistributionSample(t *testing.T) {
	t.Parallel()

	t.Run("samples within expected range", func(t *testing.T) {
		t.Parallel()
		d := Distribution{Mean: 30 * time.Millisecond, StdDev: 10 * time.Millisecond}
		rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

		for range 1000 {
			sample := d.Sample(rng)
			assert.GreaterOrEqual(t, sample, time.Duration(0), "sample must not be negative")
		}
	})

	t.Run("zero stddev returns mean", func(t *testing.T) {
		t.Parallel()
		d := Distribution{Mean: 50 * time.Millisecond, StdDev: 0}
		rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

		for range 100 {
			assert.Equal(t, 50*time.Millisecond, d.Sample(rng))
		}
	})

	t.Run("samples cluster around mean", func(t *testing.T) {
		t.Parallel()
		d := Distribution{Mean: 100 * time.Millisecond, StdDev: 10 * time.Millisecond}
		rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

		var total time.Duration
		n := 10000
		for range n {
			total += d.Sample(rng)
		}
		avg := total / time.Duration(n)
		// Average should be within 5ms of mean with 10k samples
		diff := avg - d.Mean
		if diff < 0 {
			diff = -diff
		}
		assert.Less(t, diff, 5*time.Millisecond, "average of samples should be close to mean")
	})
}

func TestParseFloatDistribution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		mean    float64
		stddev  float64
		wantErr string
	}{
		{
			name:   "fixed value",
			input:  "0.65",
			mean:   0.65,
			stddev: 0,
		},
		{
			name:   "with +/-",
			input:  "0.65 +/- 0.1",
			mean:   0.65,
			stddev: 0.1,
		},
		{
			name:   "with unicode ±",
			input:  "0.65 ± 0.1",
			mean:   0.65,
			stddev: 0.1,
		},
		{
			name:   "integer value",
			input:  "100",
			mean:   100,
			stddev: 0,
		},
		{
			name:   "integer with variance",
			input:  "100 +/- 10",
			mean:   100,
			stddev: 10,
		},
		{
			name:   "extra whitespace",
			input:  "  0.5  +/-  0.2  ",
			mean:   0.5,
			stddev: 0.2,
		},
		{
			name:   "negative mean",
			input:  "-1.5",
			mean:   -1.5,
			stddev: 0,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: "value is required",
		},
		{
			name:    "invalid mean",
			input:   "abc",
			wantErr: "invalid mean value",
		},
		{
			name:    "invalid stddev",
			input:   "0.5 +/- xyz",
			wantErr: "invalid stddev value",
		},
		{
			name:    "negative stddev",
			input:   "0.5 +/- -0.1",
			wantErr: "stddev must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d, err := ParseFloatDistribution(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.mean, d.Mean, 1e-9)
			assert.InDelta(t, tt.stddev, d.StdDev, 1e-9)
		})
	}
}

func TestFloatDistributionSample(t *testing.T) {
	t.Parallel()

	t.Run("zero stddev returns mean", func(t *testing.T) {
		t.Parallel()
		d := FloatDistribution{Mean: 0.65, StdDev: 0}
		rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing
		for range 100 {
			assert.Equal(t, 0.65, d.Sample(rng))
		}
	})

	t.Run("samples cluster around mean", func(t *testing.T) {
		t.Parallel()
		d := FloatDistribution{Mean: 0.65, StdDev: 0.1}
		rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

		var total float64
		n := 10000
		for range n {
			total += d.Sample(rng)
		}
		avg := total / float64(n)
		assert.InDelta(t, 0.65, avg, 0.01, "average should be close to mean")
	})
}

func TestFloatDistributionString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "0.65", FloatDistribution{Mean: 0.65}.String())
	assert.Equal(t, "0.65 +/- 0.1", FloatDistribution{Mean: 0.65, StdDev: 0.1}.String())
	assert.Equal(t, "100 +/- 10", FloatDistribution{Mean: 100, StdDev: 10}.String())
}

func TestDistributionString(t *testing.T) {
	t.Parallel()

	d := Distribution{Mean: 30 * time.Millisecond, StdDev: 10 * time.Millisecond}
	assert.Equal(t, "30ms +/- 10ms", d.String())

	d2 := Distribution{Mean: 50 * time.Millisecond, StdDev: 0}
	assert.Equal(t, "50ms", d2.String())
}
