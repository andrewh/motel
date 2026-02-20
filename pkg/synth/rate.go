// Rate parsing for traffic configuration
// Parses rate strings like "10/s", "5/m", "100/h" into a count and period
package synth

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Rate represents requests per time period.
type Rate struct {
	count  int
	period time.Duration
}

// ParseRate creates a Rate from a string like "10/s", "5/m", "100/h".
func ParseRate(s string) (Rate, error) {
	if s == "" {
		return Rate{}, fmt.Errorf("rate cannot be empty")
	}

	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return Rate{}, fmt.Errorf("invalid rate format (expected 'N/unit')")
	}

	count, err := strconv.Atoi(parts[0])
	if err != nil {
		return Rate{}, fmt.Errorf("invalid rate count: %w", err)
	}
	if count <= 0 {
		return Rate{}, fmt.Errorf("rate count must be positive")
	}
	if count > 10000 {
		return Rate{}, fmt.Errorf("rate count cannot exceed 10000")
	}

	period, err := parseRatePeriod(parts[1])
	if err != nil {
		return Rate{}, err
	}

	return Rate{count: count, period: period}, nil
}

func parseRatePeriod(unit string) (time.Duration, error) {
	switch strings.ToLower(unit) {
	case "s", "sec", "second", "seconds":
		return time.Second, nil
	case "m", "min", "minute", "minutes":
		return time.Minute, nil
	case "h", "hour", "hours":
		return time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported rate unit '%s', supported units: s, m, h", unit)
	}
}

// Count returns the number of requests.
func (r Rate) Count() int {
	return r.count
}

// Period returns the time period.
func (r Rate) Period() time.Duration {
	return r.period
}
