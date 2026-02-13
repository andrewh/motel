// Scenario activation and override resolution for time-windowed behaviour changes
// Parses time offsets, determines active scenarios, and merges overlapping overrides
package synth

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Scenario is a resolved, time-windowed set of operation overrides.
type Scenario struct {
	Name      string
	Start     time.Duration
	End       time.Duration
	Priority  int
	Overrides map[string]Override
	Traffic   TrafficPattern
}

// Override holds resolved per-operation overrides within a scenario.
type Override struct {
	Duration     Distribution
	ErrorRate    float64
	HasErrorRate bool
	Attributes   map[string]AttributeGenerator
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
			if len(ov.Attributes) > 0 {
				o.Attributes = make(map[string]AttributeGenerator, len(ov.Attributes))
				for attrName, attrCfg := range ov.Attributes {
					gen, genErr := NewAttributeGenerator(attrCfg)
					if genErr != nil {
						return nil, fmt.Errorf("scenario %q override %q: attribute %q: %w", cfg.Name, ref, attrName, genErr)
					}
					o.Attributes[attrName] = gen
				}
			}
			overrides[ref] = o
		}

		var traffic TrafficPattern
		if cfg.Traffic != nil {
			traffic, err = NewTrafficPattern(*cfg.Traffic)
			if err != nil {
				return nil, fmt.Errorf("scenario %q: traffic: %w", cfg.Name, err)
			}
		}

		scenarios = append(scenarios, Scenario{
			Name:      cfg.Name,
			Start:     start,
			End:       start + dur,
			Priority:  cfg.Priority,
			Overrides: overrides,
			Traffic:   traffic,
		})
	}
	return scenarios, nil
}

// ActiveScenarios returns scenarios whose activation window contains the given elapsed time.
// Results are stable-sorted by priority (ascending) so higher-priority scenarios are
// processed last in ResolveOverrides and their values win.
func ActiveScenarios(scenarios []Scenario, elapsed time.Duration) []Scenario {
	var active []Scenario
	for i := range scenarios {
		if elapsed >= scenarios[i].Start && elapsed < scenarios[i].End {
			active = append(active, scenarios[i])
		}
	}
	slices.SortStableFunc(active, func(a, b Scenario) int {
		return cmp.Compare(a.Priority, b.Priority)
	})
	return active
}

// ResolveOverrides merges overrides from multiple active scenarios.
// Later scenarios override earlier ones (last-defined-wins), but only for
// fields that are explicitly set. Attributes are merged per-key.
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
			if len(ov.Attributes) > 0 {
				if existing.Attributes == nil {
					existing.Attributes = make(map[string]AttributeGenerator, len(ov.Attributes))
				}
				maps.Copy(existing.Attributes, ov.Attributes)
			}
			merged[ref] = existing
		}
	}
	return merged
}

// ResolveTraffic returns the traffic pattern from the highest-priority active scenario
// that has a traffic override, or nil if none do.
func ResolveTraffic(active []Scenario) TrafficPattern {
	var result TrafficPattern
	for _, sc := range active {
		if sc.Traffic != nil {
			result = sc.Traffic
		}
	}
	return result
}
