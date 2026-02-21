// Duration distribution parsing and sampling for synthetic telemetry
// Supports the "30ms +/- 10ms" DSL format with normal distribution sampling
package synth

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// Distribution represents a duration with optional variance, sampled as a normal distribution.
type Distribution struct {
	Mean   time.Duration
	StdDev time.Duration
}

// ParseDistribution parses a duration distribution string.
// Supported formats:
//   - "30ms +/- 10ms" (mean with standard deviation)
//   - "30ms ± 10ms"   (unicode variant)
//   - "50ms"           (fixed duration, zero variance)
func ParseDistribution(s string) (Distribution, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Distribution{}, fmt.Errorf("duration is required (e.g. '50ms', '1s +/- 200ms')")
	}

	// Try splitting on "+/-" or "±"
	var meanStr, stddevStr string
	if parts := strings.SplitN(s, "+/-", 2); len(parts) == 2 {
		meanStr = strings.TrimSpace(parts[0])
		stddevStr = strings.TrimSpace(parts[1])
	} else if parts := strings.SplitN(s, "±", 2); len(parts) == 2 {
		meanStr = strings.TrimSpace(parts[0])
		stddevStr = strings.TrimSpace(parts[1])
	} else {
		// Fixed duration, no variance
		mean, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return Distribution{}, fmt.Errorf("invalid mean duration: %w", err)
		}
		if mean <= 0 {
			return Distribution{}, fmt.Errorf("mean duration must be positive")
		}
		return Distribution{Mean: mean}, nil
	}

	mean, err := time.ParseDuration(meanStr)
	if err != nil {
		return Distribution{}, fmt.Errorf("invalid mean duration: %w", err)
	}
	if mean <= 0 {
		return Distribution{}, fmt.Errorf("mean duration must be positive")
	}

	stddev, err := time.ParseDuration(stddevStr)
	if err != nil {
		return Distribution{}, fmt.Errorf("invalid stddev duration: %w", err)
	}
	if stddev < 0 {
		return Distribution{}, fmt.Errorf("stddev must not be negative")
	}

	return Distribution{Mean: mean, StdDev: stddev}, nil
}

// Sample returns a duration drawn from a normal distribution, clamped to minimum zero.
func (d Distribution) Sample(rng *rand.Rand) time.Duration {
	if d.StdDev == 0 {
		return d.Mean
	}
	sample := float64(d.Mean) + rng.NormFloat64()*float64(d.StdDev)
	if sample < 0 {
		sample = 0
	}
	return time.Duration(sample)
}

// String returns the distribution in DSL format.
func (d Distribution) String() string {
	if d.StdDev == 0 {
		return d.Mean.String()
	}
	return fmt.Sprintf("%s +/- %s", d.Mean, d.StdDev)
}
