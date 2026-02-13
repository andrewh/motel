// YAML DSL configuration types, loading, and validation for synthetic topology
// Parses service definitions, traffic patterns, and scenario overrides
package synth

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level YAML configuration for a synthetic topology.
type Config struct {
	Services  []ServiceConfig  `yaml:"-"`
	Traffic   TrafficConfig    `yaml:"traffic"`
	Scenarios []ScenarioConfig `yaml:"scenarios,omitempty"`
}

// rawConfig mirrors Config but uses a map for services to match the YAML structure.
type rawConfig struct {
	Services  map[string]rawServiceConfig `yaml:"services"`
	Traffic   TrafficConfig               `yaml:"traffic"`
	Scenarios []ScenarioConfig            `yaml:"scenarios,omitempty"`
}

// rawServiceConfig is the YAML representation of a service before normalisation.
type rawServiceConfig struct {
	Attributes map[string]string             `yaml:"attributes,omitempty"`
	Operations map[string]rawOperationConfig `yaml:"operations"`
}

// CallConfig describes a downstream call in the YAML DSL.
// Supports both simple string form ("service.op") and rich mapping form.
type CallConfig struct {
	Target      string  `yaml:"target"`
	Probability float64 `yaml:"probability,omitempty"`
	Condition   string  `yaml:"condition,omitempty"`
	Count       int     `yaml:"count,omitempty"`
}

// UnmarshalYAML handles both scalar string and mapping forms for call config.
func (c *CallConfig) UnmarshalYAML(unmarshal func(any) error) error {
	var scalar string
	if err := unmarshal(&scalar); err == nil {
		c.Target = scalar
		return nil
	}

	type plain CallConfig
	var p plain
	if err := unmarshal(&p); err != nil {
		return fmt.Errorf("call: expected string or mapping with target: %w", err)
	}
	*c = CallConfig(p)
	return nil
}

// rawOperationConfig is the YAML representation of an operation before normalisation.
type rawOperationConfig struct {
	Domain     string                          `yaml:"domain,omitempty"`
	Duration   string                          `yaml:"duration"`
	ErrorRate  string                          `yaml:"error_rate,omitempty"`
	Calls      []CallConfig                    `yaml:"calls,omitempty"`
	CallStyle  string                          `yaml:"call_style,omitempty"`
	Attributes map[string]AttributeValueConfig `yaml:"attributes,omitempty"`
}

// ServiceConfig describes a service in the topology.
type ServiceConfig struct {
	Name       string
	Attributes map[string]string
	Operations []OperationConfig
}

// OperationConfig describes an operation within a service.
type OperationConfig struct {
	Name       string
	Domain     string
	Duration   string
	ErrorRate  string
	Calls      []CallConfig
	CallStyle  string
	Attributes map[string]AttributeValueConfig
}

// TrafficConfig describes the traffic generation pattern.
type TrafficConfig struct {
	Rate             string          `yaml:"rate"`
	Pattern          string          `yaml:"pattern,omitempty"`
	BurstMultiplier  float64         `yaml:"burst_multiplier,omitempty"`
	BurstInterval    string          `yaml:"burst_interval,omitempty"`
	BurstDuration    string          `yaml:"burst_duration,omitempty"`
	PeakMultiplier   float64         `yaml:"peak_multiplier,omitempty"`
	TroughMultiplier float64         `yaml:"trough_multiplier,omitempty"`
	Period           string          `yaml:"period,omitempty"`
	Segments         []SegmentConfig `yaml:"segments,omitempty"`
	Overlay          *TrafficConfig  `yaml:"overlay,omitempty"`
}

// SegmentConfig describes a time-bounded rate segment in a custom traffic pattern.
type SegmentConfig struct {
	Until string `yaml:"until"`
	Rate  string `yaml:"rate"`
}

// ScenarioConfig describes a time-windowed override to operation behaviour.
type ScenarioConfig struct {
	Name     string                    `yaml:"name"`
	At       string                    `yaml:"at"`
	Duration string                    `yaml:"duration"`
	Priority int                       `yaml:"priority,omitempty"`
	Override map[string]OverrideConfig `yaml:"override"`
}

// OverrideConfig holds per-operation overrides within a scenario.
type OverrideConfig struct {
	Duration   string                          `yaml:"duration,omitempty"`
	ErrorRate  string                          `yaml:"error_rate,omitempty"`
	Attributes map[string]AttributeValueConfig `yaml:"attributes,omitempty"`
}

// LoadConfig reads and parses a YAML configuration file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // user-supplied config path is expected
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg := &Config{
		Traffic:   raw.Traffic,
		Scenarios: raw.Scenarios,
	}

	// Convert map-based services into ordered slice (sorted for determinism)
	serviceNames := make([]string, 0, len(raw.Services))
	for name := range raw.Services {
		serviceNames = append(serviceNames, name)
	}
	slices.Sort(serviceNames)

	for _, name := range serviceNames {
		rawSvc := raw.Services[name]
		svc := ServiceConfig{
			Name:       name,
			Attributes: rawSvc.Attributes,
		}

		opNames := make([]string, 0, len(rawSvc.Operations))
		for opName := range rawSvc.Operations {
			opNames = append(opNames, opName)
		}
		slices.Sort(opNames)

		for _, opName := range opNames {
			rawOp := rawSvc.Operations[opName]
			svc.Operations = append(svc.Operations, OperationConfig{
				Name:       opName,
				Domain:     rawOp.Domain,
				Duration:   rawOp.Duration,
				ErrorRate:  rawOp.ErrorRate,
				Calls:      rawOp.Calls,
				CallStyle:  rawOp.CallStyle,
				Attributes: rawOp.Attributes,
			})
		}
		cfg.Services = append(cfg.Services, svc)
	}

	return cfg, nil
}

// ValidateConfig checks a configuration for structural correctness.
func ValidateConfig(cfg *Config) error {
	if len(cfg.Services) == 0 {
		return fmt.Errorf("at least one service is required")
	}

	// Build a lookup of all known operations for reference validation
	knownOps := make(map[string]bool)
	for _, svc := range cfg.Services {
		if len(svc.Operations) == 0 {
			return fmt.Errorf("service %q must have at least one operation", svc.Name)
		}
		for _, op := range svc.Operations {
			knownOps[svc.Name+"."+op.Name] = true
		}
	}

	// Validate each operation
	for _, svc := range cfg.Services {
		for _, op := range svc.Operations {
			if _, err := ParseDistribution(op.Duration); err != nil {
				return fmt.Errorf("service %q operation %q: invalid duration: %w", svc.Name, op.Name, err)
			}

			if op.ErrorRate != "" {
				if _, err := parseErrorRate(op.ErrorRate); err != nil {
					return fmt.Errorf("service %q operation %q: invalid error_rate: %w", svc.Name, op.Name, err)
				}
			}

			if op.CallStyle != "" && op.CallStyle != "parallel" && op.CallStyle != "sequential" {
				return fmt.Errorf("service %q operation %q: call_style must be \"parallel\" or \"sequential\", got %q", svc.Name, op.Name, op.CallStyle)
			}

			for attrName, attrCfg := range op.Attributes {
				if _, err := NewAttributeGenerator(attrCfg); err != nil {
					return fmt.Errorf("service %q operation %q: attribute %q: %w", svc.Name, op.Name, attrName, err)
				}
			}

			for _, call := range op.Calls {
				if !strings.Contains(call.Target, ".") {
					return fmt.Errorf("service %q operation %q: call %q must be in service.operation format", svc.Name, op.Name, call.Target)
				}
				if !knownOps[call.Target] {
					return fmt.Errorf("service %q operation %q: call %q references unknown operation", svc.Name, op.Name, call.Target)
				}
				if call.Probability < 0 || call.Probability > 1 {
					return fmt.Errorf("service %q operation %q: call %q probability must be between 0 and 1", svc.Name, op.Name, call.Target)
				}
				if call.Condition != "" && call.Condition != "on-error" && call.Condition != "on-success" {
					return fmt.Errorf("service %q operation %q: call %q condition must be \"on-error\" or \"on-success\", got %q", svc.Name, op.Name, call.Target, call.Condition)
				}
				if call.Count < 0 {
					return fmt.Errorf("service %q operation %q: call %q count must not be negative", svc.Name, op.Name, call.Target)
				}
			}
		}
	}

	if err := validateTrafficConfig(cfg.Traffic, false); err != nil {
		return err
	}

	// Validate scenarios
	for _, sc := range cfg.Scenarios {
		if _, err := ParseOffset(sc.At); err != nil {
			return fmt.Errorf("scenario %q: invalid at: %w", sc.Name, err)
		}
		if _, err := time.ParseDuration(sc.Duration); err != nil {
			return fmt.Errorf("scenario %q: invalid duration: %w", sc.Name, err)
		}
		for ref, override := range sc.Override {
			if !knownOps[ref] {
				return fmt.Errorf("scenario %q: override %q references unknown operation", sc.Name, ref)
			}
			if override.Duration != "" {
				if _, err := ParseDistribution(override.Duration); err != nil {
					return fmt.Errorf("scenario %q: override %q: invalid duration: %w", sc.Name, ref, err)
				}
			}
			if override.ErrorRate != "" {
				if _, err := parseErrorRate(override.ErrorRate); err != nil {
					return fmt.Errorf("scenario %q: override %q: invalid error_rate: %w", sc.Name, ref, err)
				}
			}
			for attrName, attrCfg := range override.Attributes {
				if _, err := NewAttributeGenerator(attrCfg); err != nil {
					return fmt.Errorf("scenario %q: override %q: attribute %q: %w", sc.Name, ref, attrName, err)
				}
			}
		}
	}

	return nil
}

func validateTrafficConfig(tc TrafficConfig, isOverlay bool) error {
	pattern := tc.Pattern
	if pattern == "" {
		pattern = "uniform"
	}

	hasBurstyFields := tc.BurstMultiplier != 0 || tc.BurstInterval != "" || tc.BurstDuration != ""
	hasDiurnalFields := tc.PeakMultiplier != 0 || tc.TroughMultiplier != 0 || tc.Period != ""
	hasSegments := len(tc.Segments) > 0

	if hasBurstyFields && pattern != "bursty" {
		return fmt.Errorf("burst_multiplier, burst_interval, burst_duration are only valid with pattern \"bursty\"")
	}
	if hasDiurnalFields && pattern != "diurnal" {
		return fmt.Errorf("peak_multiplier, trough_multiplier, period are only valid with pattern \"diurnal\"")
	}
	if hasSegments && pattern != "custom" {
		return fmt.Errorf("segments are only valid with pattern \"custom\"")
	}

	if _, err := newBasePattern(tc); err != nil {
		return err
	}

	if tc.Overlay != nil {
		if isOverlay {
			return fmt.Errorf("nested overlay is not supported")
		}
		if err := validateTrafficConfig(*tc.Overlay, true); err != nil {
			return fmt.Errorf("overlay: %w", err)
		}
	}

	return nil
}

// parseErrorRate parses a percentage string like "0.1%" or "15%" into a float64 (0.0 to 1.0).
func parseErrorRate(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if pct, ok := strings.CutSuffix(s, "%"); ok {
		v, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid error_rate %q: %w", pct, err)
		}
		if v < 0 || v > 100 {
			return 0, fmt.Errorf("error_rate must be between 0%% and 100%%")
		}
		return v / 100, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid error_rate %q: %w", s, err)
	}
	if v < 0 || v > 1 {
		return 0, fmt.Errorf("error_rate without %% must be between 0.0 and 1.0")
	}
	return v, nil
}
