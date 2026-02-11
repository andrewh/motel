// Scenario activation and override resolution for time-windowed behaviour changes
// Parses time offsets, determines active scenarios, and merges overlapping overrides
package synth

import (
	"fmt"
	"strings"
	"time"
)

// Scenario is a resolved, time-windowed set of operation overrides.
type Scenario struct {
	Name      string
	Start     time.Duration
	End       time.Duration
	Overrides map[string]Override
}

// Override holds resolved per-operation overrides within a scenario.
type Override struct {
	Duration     Distribution
	ErrorRate    float64
	HasErrorRate bool
}

// ParseOffset parses a time offset string like "+5m" or "30s" into a duration.
func ParseOffset(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("offset cannot be empty")
	}
	s = strings.TrimPrefix(s, "+")
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid offset %q: %w", s, err)
	}
	return d, nil
}

// BuildScenarios converts scenario configs into resolved Scenarios.
func BuildScenarios(cfgs []ScenarioConfig) ([]Scenario, error) {
	scenarios := make([]Scenario, 0, len(cfgs))
	for _, cfg := range cfgs {
		start, err := ParseOffset(cfg.At)
		if err != nil {
			return nil, fmt.Errorf("scenario %q: invalid at: %w", cfg.Name, err)
		}
		dur, err := time.ParseDuration(cfg.Duration)
		if err != nil {
			return nil, fmt.Errorf("scenario %q: invalid duration: %w", cfg.Name, err)
		}

		overrides := make(map[string]Override, len(cfg.Override))
		for ref, ov := range cfg.Override {
			var o Override
			if ov.Duration != "" {
				o.Duration, err = ParseDistribution(ov.Duration)
				if err != nil {
					return nil, fmt.Errorf("scenario %q override %q: %w", cfg.Name, ref, err)
				}
			}
			if ov.ErrorRate != "" {
				o.ErrorRate, err = parseErrorRate(ov.ErrorRate)
				if err != nil {
					return nil, fmt.Errorf("scenario %q override %q: %w", cfg.Name, ref, err)
				}
				o.HasErrorRate = true
			}
			overrides[ref] = o
		}

		scenarios = append(scenarios, Scenario{
			Name:      cfg.Name,
			Start:     start,
			End:       start + dur,
			Overrides: overrides,
		})
	}
	return scenarios, nil
}

// ActiveScenarios returns scenarios whose activation window contains the given elapsed time.
func ActiveScenarios(scenarios []Scenario, elapsed time.Duration) []Scenario {
	var active []Scenario
	for i := range scenarios {
		if elapsed >= scenarios[i].Start && elapsed < scenarios[i].End {
			active = append(active, scenarios[i])
		}
	}
	return active
}

// ResolveOverrides merges overrides from multiple active scenarios.
// Later scenarios override earlier ones (last-defined-wins), but only for
// fields that are explicitly set.
func ResolveOverrides(active []Scenario) map[string]Override {
	merged := make(map[string]Override)
	for _, sc := range active {
		for ref, ov := range sc.Overrides {
			existing, ok := merged[ref]
			if !ok {
				merged[ref] = ov
				continue
			}
			if ov.Duration.Mean > 0 {
				existing.Duration = ov.Duration
			}
			if ov.HasErrorRate {
				existing.ErrorRate = ov.ErrorRate
				existing.HasErrorRate = true
			}
			merged[ref] = existing
		}
	}
	return merged
}
