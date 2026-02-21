// YAML serialisation of inferred config using the map-based form matching the synth DSL
// Produces output compatible with synth.LoadConfig for round-trip validation
package traceimport

import (
	"bytes"
	"fmt"
	"math"
	"sort"

	"gopkg.in/yaml.v3"
)

// inferredConfig is the map-based YAML structure matching the synth DSL.
type inferredConfig struct {
	Version  int                        `yaml:"version"`
	Services map[string]inferredService `yaml:"services"`
	Traffic  map[string]string          `yaml:"traffic"`
}

type inferredService struct {
	Attributes map[string]string            `yaml:"attributes,omitempty"`
	Operations map[string]inferredOperation `yaml:"operations"`
}

type inferredOperation struct {
	Duration  string `yaml:"duration"`
	ErrorRate string `yaml:"error_rate,omitempty"`
	CallStyle string `yaml:"call_style,omitempty"`
	Calls     []any  `yaml:"calls,omitempty"`
}

// inferredCallRich is the mapping form when probability is needed.
type inferredCallRich struct {
	Target      string  `yaml:"target"`
	Probability float64 `yaml:"probability"`
}

// MarshalConfig produces YAML bytes from the collected statistics.
func MarshalConfig(collector *StatsCollector, serviceAttrs map[string]map[string]string, traceCount int, spanCount int, windowSecs float64) ([]byte, error) {
	cfg := inferredConfig{
		Version:  1,
		Services: make(map[string]inferredService),
		Traffic:  make(map[string]string),
	}

	// Sort service names for deterministic output
	svcNames := make([]string, 0, len(collector.Services))
	for name := range collector.Services {
		svcNames = append(svcNames, name)
	}
	sort.Strings(svcNames)

	for _, svcName := range svcNames {
		svcStats := collector.Services[svcName]
		svc := inferredService{
			Operations: make(map[string]inferredOperation),
		}

		if attrs, ok := serviceAttrs[svcName]; ok && len(attrs) > 0 {
			svc.Attributes = attrs
		}

		opNames := make([]string, 0, len(svcStats.Ops))
		for name := range svcStats.Ops {
			opNames = append(opNames, name)
		}
		sort.Strings(opNames)

		for _, opName := range opNames {
			opStats := svcStats.Ops[opName]
			op := inferredOperation{
				Duration:  FormatDuration(opStats.Durations),
				ErrorRate: FormatErrorRate(opStats.ErrorCount, opStats.TotalCount),
			}

			// Call style: only set if sequential (parallel is the default)
			if vote, ok := svcStats.CallStyles[opName]; ok {
				if vote.Sequential > vote.Parallel {
					op.CallStyle = "sequential"
				}
			}

			// Calls
			if len(opStats.Calls) > 0 {
				callTargets := make([]string, 0, len(opStats.Calls))
				for target := range opStats.Calls {
					callTargets = append(callTargets, target)
				}
				sort.Strings(callTargets)

				for _, target := range callTargets {
					cs := opStats.Calls[target]
					prob := 1.0
					if opStats.TotalCount > 0 {
						prob = float64(cs.Count) / float64(opStats.TotalCount)
					}
					if prob >= 1.0 {
						op.Calls = append(op.Calls, target)
					} else {
						op.Calls = append(op.Calls, inferredCallRich{
							Target:      target,
							Probability: roundFloat(prob, 2),
						})
					}
				}
			}

			svc.Operations[opName] = op
		}

		cfg.Services[svcName] = svc
	}

	// Traffic rate
	if windowSecs > 0 && traceCount > 1 {
		rate := float64(traceCount) / windowSecs
		if rate > 10000 {
			rate = 10000
		}
		if rate >= 1.0 {
			cfg.Traffic["rate"] = fmt.Sprintf("%.0f/s", rate)
		} else {
			// Sub-1/s rates: convert to per-minute to stay integer
			perMin := rate * 60
			if perMin >= 1.0 {
				cfg.Traffic["rate"] = fmt.Sprintf("%.0f/m", perMin)
			} else {
				// Extremely low rate: use 1/m as floor
				cfg.Traffic["rate"] = "1/m"
			}
		}
	} else {
		cfg.Traffic["rate"] = "1/s"
	}

	// Marshal to YAML with 2-space indent to match existing synth configs
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&cfg); err != nil {
		return nil, fmt.Errorf("marshalling YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("closing YAML encoder: %w", err)
	}
	data := buf.Bytes()

	// Prepend header comment
	header := fmt.Sprintf("# Inferred from %d traces (%d spans) observed over %.0f seconds\n# Review and adjust durations, error rates, and call probabilities as needed\n\n",
		traceCount, spanCount, windowSecs)

	return append([]byte(header), data...), nil
}

// roundFloat rounds a float to n decimal places.
func roundFloat(f float64, n int) float64 {
	shift := 1.0
	for range n {
		shift *= 10
	}
	return math.Round(f*shift) / shift
}
