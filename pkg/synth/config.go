// YAML DSL configuration types, loading, and validation for synthetic topology
// Parses service definitions, traffic patterns, and scenario overrides
package synth

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/andrewh/motel/pkg/models"
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

// rawOperationConfig is the YAML representation of an operation before normalisation.
type rawOperationConfig struct {
	Duration  string   `yaml:"duration"`
	ErrorRate string   `yaml:"error_rate,omitempty"`
	Calls     []string `yaml:"calls,omitempty"`
}

// ServiceConfig describes a service in the topology.
type ServiceConfig struct {
	Name       string
	Attributes map[string]string
	Operations []OperationConfig
}

// OperationConfig describes an operation within a service.
type OperationConfig struct {
	Name      string
	Duration  string
	ErrorRate string
	Calls     []string
}

// TrafficConfig describes the traffic generation pattern.
type TrafficConfig struct {
	Rate    string `yaml:"rate"`
	Pattern string `yaml:"pattern,omitempty"`
}

// ScenarioConfig describes a time-windowed override to operation behaviour.
type ScenarioConfig struct {
	Name     string                    `yaml:"name"`
	At       string                    `yaml:"at"`
	Duration string                    `yaml:"duration"`
	Override map[string]OverrideConfig `yaml:"override"`
}

// OverrideConfig holds per-operation overrides within a scenario.
type OverrideConfig struct {
	Duration  string `yaml:"duration,omitempty"`
	ErrorRate string `yaml:"error_rate,omitempty"`
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
				Name:      opName,
				Duration:  rawOp.Duration,
				ErrorRate: rawOp.ErrorRate,
				Calls:     rawOp.Calls,
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

			for _, call := range op.Calls {
				if !strings.Contains(call, ".") {
					return fmt.Errorf("service %q operation %q: call %q must be in service.operation format", svc.Name, op.Name, call)
				}
				if !knownOps[call] {
					return fmt.Errorf("service %q operation %q: call %q references unknown operation", svc.Name, op.Name, call)
				}
			}
		}
	}

	// Validate traffic rate
	if _, err := models.NewRate(cfg.Traffic.Rate); err != nil {
		return fmt.Errorf("invalid traffic rate: %w", err)
	}

	// Validate scenarios
	for _, sc := range cfg.Scenarios {
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
