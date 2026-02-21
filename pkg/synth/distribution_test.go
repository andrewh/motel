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

func TestDistributionString(t *testing.T) {
	t.Parallel()

	d := Distribution{Mean: 30 * time.Millisecond, StdDev: 10 * time.Millisecond}
	assert.Equal(t, "30ms +/- 10ms", d.String())

	d2 := Distribution{Mean: 50 * time.Millisecond, StdDev: 0}
	assert.Equal(t, "50ms", d2.String())
}
