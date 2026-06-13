package synth

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// CheckAssertions is the user-facing YAML format consumed by motel check.
type CheckAssertions struct {
	Version int
	Checks  CheckThresholds
}

type rawCheckAssertions struct {
	Version *int             `yaml:"version"`
	Checks  *CheckThresholds `yaml:"checks"`
}

// CheckThresholds contains structural limits users can load from a checks file.
type CheckThresholds struct {
	MaxDepth  *int `yaml:"max_depth,omitempty"`
	MaxFanOut *int `yaml:"max_fan_out,omitempty"`
	MaxSpans  *int `yaml:"max_spans,omitempty"`
	P50Depth  *int `yaml:"p50_depth,omitempty"`
	P95Depth  *int `yaml:"p95_depth,omitempty"`
	P99Depth  *int `yaml:"p99_depth,omitempty"`
	P50FanOut *int `yaml:"p50_fan_out,omitempty"`
	P95FanOut *int `yaml:"p95_fan_out,omitempty"`
	P99FanOut *int `yaml:"p99_fan_out,omitempty"`
	P50Spans  *int `yaml:"p50_spans,omitempty"`
	P95Spans  *int `yaml:"p95_spans,omitempty"`
	P99Spans  *int `yaml:"p99_spans,omitempty"`
}

// LoadCheckAssertions reads and validates a YAML checks file from a file path or URL.
func LoadCheckAssertions(source string) (*CheckAssertions, error) {
	data, err := readSource(source)
	if err != nil {
		return nil, fmt.Errorf("reading checks: %w", err)
	}

	var raw rawCheckAssertions
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing checks: %w", err)
	}

	if raw.Version == nil {
		return nil, fmt.Errorf("missing required field: version (e.g. 'version: 1')")
	}
	if *raw.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported checks version %d (supported: %d)", *raw.Version, CurrentVersion)
	}
	if raw.Checks == nil || !raw.Checks.HasAny() {
		return nil, fmt.Errorf("checks section must define at least one threshold")
	}
	if err := raw.Checks.validate(); err != nil {
		return nil, err
	}

	return &CheckAssertions{
		Version: *raw.Version,
		Checks:  *raw.Checks,
	}, nil
}

// HasAny reports whether at least one threshold is configured.
func (t CheckThresholds) HasAny() bool {
	return t.MaxDepth != nil ||
		t.MaxFanOut != nil ||
		t.MaxSpans != nil ||
		t.HasPercentile()
}

// HasPercentile reports whether sampled percentile thresholds are configured.
func (t CheckThresholds) HasPercentile() bool {
	return t.P50Depth != nil ||
		t.P95Depth != nil ||
		t.P99Depth != nil ||
		t.P50FanOut != nil ||
		t.P95FanOut != nil ||
		t.P99FanOut != nil ||
		t.P50Spans != nil ||
		t.P95Spans != nil ||
		t.P99Spans != nil
}

func (t CheckThresholds) percentileCount() int {
	count := 0
	for _, v := range []*int{
		t.P50Depth,
		t.P95Depth,
		t.P99Depth,
		t.P50FanOut,
		t.P95FanOut,
		t.P99FanOut,
		t.P50Spans,
		t.P95Spans,
		t.P99Spans,
	} {
		if v != nil {
			count++
		}
	}
	return count
}

func (t CheckThresholds) validate() error {
	for _, threshold := range []struct {
		name  string
		value *int
	}{
		{"max_depth", t.MaxDepth},
		{"max_fan_out", t.MaxFanOut},
		{"max_spans", t.MaxSpans},
		{"p50_depth", t.P50Depth},
		{"p95_depth", t.P95Depth},
		{"p99_depth", t.P99Depth},
		{"p50_fan_out", t.P50FanOut},
		{"p95_fan_out", t.P95FanOut},
		{"p99_fan_out", t.P99FanOut},
		{"p50_spans", t.P50Spans},
		{"p95_spans", t.P95Spans},
		{"p99_spans", t.P99Spans},
	} {
		if threshold.value != nil && *threshold.value < 0 {
			return fmt.Errorf("checks.%s must be non-negative", threshold.name)
		}
	}
	return nil
}
