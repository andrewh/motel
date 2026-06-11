// Duration distribution parsing and sampling for synthetic telemetry
// Supports the "30ms +/- 10ms" DSL format with normal distribution sampling
package synth

import (
	"fmt"
	"math/rand/v2"
	"strconv"
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

// FloatDistribution represents a numeric value with optional variance, sampled as a normal distribution.
type FloatDistribution struct {
	Mean   float64
	StdDev float64
}

// ParseFloatDistribution parses a float distribution string.
// Supported formats:
//   - "0.65 +/- 0.1" (mean with standard deviation)
//   - "0.65 ± 0.1"   (unicode variant)
//   - "0.65"          (fixed value, zero variance)
func ParseFloatDistribution(s string) (FloatDistribution, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return FloatDistribution{}, fmt.Errorf("value is required (e.g. '0.65', '0.65 +/- 0.1')")
	}

	var meanStr, stddevStr string
	if parts := strings.SplitN(s, "+/-", 2); len(parts) == 2 {
		meanStr = strings.TrimSpace(parts[0])
		stddevStr = strings.TrimSpace(parts[1])
	} else if parts := strings.SplitN(s, "±", 2); len(parts) == 2 {
		meanStr = strings.TrimSpace(parts[0])
		stddevStr = strings.TrimSpace(parts[1])
	} else {
		mean, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return FloatDistribution{}, fmt.Errorf("invalid mean value: %w", err)
		}
		return FloatDistribution{Mean: mean}, nil
	}

	mean, err := strconv.ParseFloat(meanStr, 64)
	if err != nil {
		return FloatDistribution{}, fmt.Errorf("invalid mean value: %w", err)
	}

	stddev, err := strconv.ParseFloat(stddevStr, 64)
	if err != nil {
		return FloatDistribution{}, fmt.Errorf("invalid stddev value: %w", err)
	}
	if stddev < 0 {
		return FloatDistribution{}, fmt.Errorf("stddev must not be negative")
	}

	return FloatDistribution{Mean: mean, StdDev: stddev}, nil
}

// Sample returns a value drawn from a normal distribution.
func (d FloatDistribution) Sample(rng *rand.Rand) float64 {
	if d.StdDev == 0 {
		return d.Mean
	}
	return d.Mean + rng.NormFloat64()*d.StdDev
}

// String returns the distribution in DSL format.
func (d FloatDistribution) String() string {
	if d.StdDev == 0 {
		return strconv.FormatFloat(d.Mean, 'f', -1, 64)
	}
	return fmt.Sprintf("%s +/- %s",
		strconv.FormatFloat(d.Mean, 'f', -1, 64),
		strconv.FormatFloat(d.StdDev, 'f', -1, 64))
}

// String returns the distribution in DSL format.
func (d Distribution) String() string {
	if d.StdDev == 0 {
		return d.Mean.String()
	}
	return fmt.Sprintf("%s +/- %s", d.Mean, d.StdDev)
}
