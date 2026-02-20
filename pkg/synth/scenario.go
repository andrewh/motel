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
	AddCalls     []Call
	RemoveCalls  map[string]bool
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
// The topology is required to resolve add_calls targets to *Operation pointers.
func BuildScenarios(cfgs []ScenarioConfig, topo *Topology) ([]Scenario, error) {
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
			for _, callCfg := range ov.AddCalls {
				_, targetOp, resolveErr := resolveRef(topo, callCfg.Target)
				if resolveErr != nil {
					return nil, fmt.Errorf("scenario %q override %q: add_calls: %w", cfg.Name, ref, resolveErr)
				}
				call := Call{
					Operation:   targetOp,
					Probability: callCfg.Probability,
					Condition:   callCfg.Condition,
					Count:       callCfg.Count,
					Retries:     callCfg.Retries,
				}
				if callCfg.Timeout != "" {
					call.Timeout, err = time.ParseDuration(callCfg.Timeout)
					if err != nil {
						return nil, fmt.Errorf("scenario %q override %q: add_calls: target %q: invalid timeout: %w", cfg.Name, ref, callCfg.Target, err)
					}
				}
				if callCfg.RetryBackoff != "" {
					call.RetryBackoff, err = time.ParseDuration(callCfg.RetryBackoff)
					if err != nil {
						return nil, fmt.Errorf("scenario %q override %q: add_calls: target %q: invalid retry_backoff: %w", cfg.Name, ref, callCfg.Target, err)
					}
				}
				o.AddCalls = append(o.AddCalls, call)
			}
			if len(ov.RemoveCalls) > 0 {
				o.RemoveCalls = make(map[string]bool, len(ov.RemoveCalls))
				for _, rc := range ov.RemoveCalls {
					o.RemoveCalls[rc.Target] = true
				}
			}
			overrides[ref] = o
		}

		scenario := Scenario{
			Name:      cfg.Name,
			Start:     start,
			End:       start + dur,
			Priority:  cfg.Priority,
			Overrides: overrides,
		}

		if cfg.Traffic != nil {
			scenario.Traffic, err = NewTrafficPattern(*cfg.Traffic)
			if err != nil {
				return nil, fmt.Errorf("scenario %q: traffic: %w", cfg.Name, err)
			}
		}

		if err := validateScenarioCycles(scenario, topo); err != nil {
			return nil, err
		}

		scenarios = append(scenarios, scenario)
	}
	return scenarios, nil
}

// HasCallChanges returns true if the override modifies the call graph.
func (o Override) HasCallChanges() bool {
	return len(o.AddCalls) > 0 || len(o.RemoveCalls) > 0
}

// validateScenarioCycles checks that a scenario's call changes do not create cycles.
func validateScenarioCycles(sc Scenario, topo *Topology) error {
	// Build effective adjacency list: base graph + adds - removes
	adj := make(map[string][]string)

	// Start with base topology edges
	for _, svc := range topo.Services {
		for _, op := range svc.Operations {
			for _, call := range op.Calls {
				adj[op.Ref] = append(adj[op.Ref], call.Operation.Ref)
			}
		}
	}

	hasChanges := false
	for ref, ov := range sc.Overrides {
		if !ov.HasCallChanges() {
			continue
		}
		hasChanges = true

		// Remove calls
		if len(ov.RemoveCalls) > 0 {
			filtered := make([]string, 0, len(adj[ref]))
			for _, target := range adj[ref] {
				if !ov.RemoveCalls[target] {
					filtered = append(filtered, target)
				}
			}
			adj[ref] = filtered
		}

		// Add calls
		for _, call := range ov.AddCalls {
			adj[ref] = append(adj[ref], call.Operation.Ref)
		}
	}

	if !hasChanges {
		return nil
	}

	// DFS cycle detection on the modified graph
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int)

	var visit func(ref string) error
	visit = func(ref string) error {
		switch state[ref] {
		case visiting:
			return fmt.Errorf("scenario %q: adding calls would create cycle involving %s", sc.Name, ref)
		case visited:
			return nil
		}
		state[ref] = visiting
		for _, target := range adj[ref] {
			if err := visit(target); err != nil {
				return err
			}
		}
		state[ref] = visited
		return nil
	}

	for _, ref := range slices.Sorted(maps.Keys(adj)) {
		if state[ref] == unvisited {
			if err := visit(ref); err != nil {
				return err
			}
		}
	}
	return nil
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
				// Struct copy; reference types (Attributes map, AddCalls slice,
				// RemoveCalls map) are shared with the source scenario.
				// Safe because callers only read the returned overrides.
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
				newAttrs := make(map[string]AttributeGenerator, len(existing.Attributes)+len(ov.Attributes))
				maps.Copy(newAttrs, existing.Attributes)
				maps.Copy(newAttrs, ov.Attributes)
				existing.Attributes = newAttrs
			}
			if len(ov.AddCalls) > 0 {
				existing.AddCalls = append(slices.Clone(existing.AddCalls), ov.AddCalls...)
			}
			if len(ov.RemoveCalls) > 0 {
				if existing.RemoveCalls == nil {
					existing.RemoveCalls = make(map[string]bool, len(ov.RemoveCalls))
				}
				maps.Copy(existing.RemoveCalls, ov.RemoveCalls)
			}
			merged[ref] = existing
		}
	}
	return merged
}

// ResolveTraffic returns the traffic pattern from the highest-priority active scenario
// that has a traffic override, or nil if none do. Expects active to be sorted ascending
// by priority (as returned by ActiveScenarios).
func ResolveTraffic(active []Scenario) TrafficPattern {
	var result TrafficPattern
	for _, sc := range active {
		if sc.Traffic != nil {
			result = sc.Traffic
		}
	}
	return result
}
