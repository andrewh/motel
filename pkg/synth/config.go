// YAML DSL configuration types, loading, and validation for synthetic topology
// Parses service definitions, traffic patterns, and scenario overrides
package synth

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxSourceBytes = 10 << 20 // 10 MB

// CurrentVersion is the supported schema version for synth topology configs.
const CurrentVersion = 1

// Config is the top-level YAML configuration for a synthetic topology.
type Config struct {
	Version   int              `yaml:"version"`
	Services  []ServiceConfig  `yaml:"-"`
	Traffic   TrafficConfig    `yaml:"traffic"`
	Scenarios []ScenarioConfig `yaml:"scenarios,omitempty"`
}

// rawConfig mirrors Config but uses a map for services to match the YAML structure.
type rawConfig struct {
	Version   *int                        `yaml:"version"`
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
	Target       string  `yaml:"target"`
	Probability  float64 `yaml:"probability,omitempty"`
	Condition    string  `yaml:"condition,omitempty"`
	Count        int     `yaml:"count,omitempty"`
	Timeout      string  `yaml:"timeout,omitempty"`
	Retries      int     `yaml:"retries,omitempty"`
	RetryBackoff string  `yaml:"retry_backoff,omitempty"`
	Async        bool    `yaml:"async,omitempty"`
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

// BackpressureConfig describes backpressure behaviour for an operation.
type BackpressureConfig struct {
	LatencyThreshold   string  `yaml:"latency_threshold"`
	DurationMultiplier float64 `yaml:"duration_multiplier,omitempty"`
	ErrorRateAdd       string  `yaml:"error_rate_add,omitempty"`
}

// CircuitBreakerConfig describes circuit breaker behaviour for an operation.
type CircuitBreakerConfig struct {
	FailureThreshold int    `yaml:"failure_threshold"`
	Window           string `yaml:"window"`
	Cooldown         string `yaml:"cooldown"`
}

// EventConfig describes a span event emitted during an operation.
type EventConfig struct {
	Name       string                          `yaml:"name"`
	Delay      string                          `yaml:"delay,omitempty"`
	Attributes map[string]AttributeValueConfig `yaml:"attributes,omitempty"`
}

// rawOperationConfig is the YAML representation of an operation before normalisation.
type rawOperationConfig struct {
	Domain         string                          `yaml:"domain,omitempty"`
	Duration       string                          `yaml:"duration"`
	ErrorRate      string                          `yaml:"error_rate,omitempty"`
	Calls          []CallConfig                    `yaml:"calls,omitempty"`
	CallStyle      string                          `yaml:"call_style,omitempty"`
	Attributes     map[string]AttributeValueConfig `yaml:"attributes,omitempty"`
	Events         []EventConfig                   `yaml:"events,omitempty"`
	QueueDepth     int                             `yaml:"queue_depth,omitempty"`
	Backpressure   *BackpressureConfig             `yaml:"backpressure,omitempty"`
	CircuitBreaker *CircuitBreakerConfig           `yaml:"circuit_breaker,omitempty"`
}

// ServiceConfig describes a service in the topology.
type ServiceConfig struct {
	Name       string
	Attributes map[string]string
	Operations []OperationConfig
}

// OperationConfig describes an operation within a service.
type OperationConfig struct {
	Name           string
	Domain         string
	Duration       string
	ErrorRate      string
	Calls          []CallConfig
	CallStyle      string
	Attributes     map[string]AttributeValueConfig
	Events         []EventConfig
	QueueDepth     int
	Backpressure   *BackpressureConfig
	CircuitBreaker *CircuitBreakerConfig
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
	Override map[string]OverrideConfig `yaml:"override,omitempty"`
	Traffic  *TrafficConfig            `yaml:"traffic,omitempty"`
}

// OverrideConfig holds per-operation overrides within a scenario.
type OverrideConfig struct {
	Duration    string                          `yaml:"duration,omitempty"`
	ErrorRate   string                          `yaml:"error_rate,omitempty"`
	Attributes  map[string]AttributeValueConfig `yaml:"attributes,omitempty"`
	AddCalls    []CallConfig                    `yaml:"add_calls,omitempty"`
	RemoveCalls []RemoveCallConfig              `yaml:"remove_calls,omitempty"`
}

// RemoveCallConfig identifies a downstream call to remove by target reference.
type RemoveCallConfig struct {
	Target string `yaml:"target"`
}

// UnmarshalYAML handles both scalar string and mapping forms for remove call config.
func (r *RemoveCallConfig) UnmarshalYAML(unmarshal func(any) error) error {
	var scalar string
	if err := unmarshal(&scalar); err == nil {
		r.Target = scalar
		return nil
	}

	type plain RemoveCallConfig
	var p plain
	if err := unmarshal(&p); err != nil {
		return fmt.Errorf("remove_calls: expected string or mapping with target: %w", err)
	}
	*r = RemoveCallConfig(p)
	return nil
}

// readSource fetches topology YAML from a URL or reads it from a local file.
// URL fetches have a 10-second timeout and a 10 MB response body limit.
func readSource(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		client := &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		}
		resp, err := client.Get(source) //nolint:gosec // user-supplied URL is expected
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", source, unwrapHTTPError(err))
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("fetching %s: HTTP %d", source, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
		if err != nil {
			return nil, fmt.Errorf("reading URL body: %w", err)
		}
		if int64(len(data)) > maxSourceBytes {
			return nil, fmt.Errorf("fetching %s: response body exceeds %d bytes", source, maxSourceBytes)
		}
		return data, nil
	}
	return os.ReadFile(source) //nolint:gosec // user-supplied config path is expected
}

// unwrapHTTPError extracts a human-readable error from nested http/url/net errors.
// Go's http.Client wraps errors as *url.Error → *net.OpError → syscall error,
// producing messages like: Get "http://...": dial tcp [::1]:1: connect: connection refused.
// This unwraps to the innermost message (e.g. "connection refused").
func unwrapHTTPError(err error) error {
	ue, ok := err.(*url.Error) //nolint:errorlint // deliberate type switch through layers
	if !ok {
		return err
	}
	if ue.Timeout() {
		return fmt.Errorf("timed out after 10s")
	}
	err = ue.Err
	if oe, ok := err.(*net.OpError); ok { //nolint:errorlint // deliberate type switch through layers
		err = oe.Err
	}
	return err
}

// LoadConfig reads and parses a YAML topology from a file path or URL.
func LoadConfig(source string) (*Config, error) {
	data, err := readSource(source)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if raw.Version == nil {
		return nil, fmt.Errorf("missing required field: version (e.g. 'version: 1')")
	}
	if *raw.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported config version %d (supported: %d)", *raw.Version, CurrentVersion)
	}

	cfg := &Config{
		Version:   *raw.Version,
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
				Name:           opName,
				Domain:         rawOp.Domain,
				Duration:       rawOp.Duration,
				ErrorRate:      rawOp.ErrorRate,
				Calls:          rawOp.Calls,
				CallStyle:      rawOp.CallStyle,
				Attributes:     rawOp.Attributes,
				Events:         rawOp.Events,
				QueueDepth:     rawOp.QueueDepth,
				Backpressure:   rawOp.Backpressure,
				CircuitBreaker: rawOp.CircuitBreaker,
			})
		}
		cfg.Services = append(cfg.Services, svc)
	}

	return cfg, nil
}

// ValidateConfig checks a configuration for structural correctness.
func ValidateConfig(cfg *Config) error {
	if len(cfg.Services) == 0 {
		return fmt.Errorf("at least one service is required under 'services:')")
	}
	if cfg.Traffic.Rate == "" {
		return fmt.Errorf("traffic section with rate is required, e.g.\n\n  traffic:\n    rate: 10/s")
	}

	// Build lookups for reference validation:
	// knownOps: all defined operations
	// opCalls: which targets each operation calls (for remove_calls validation)
	knownOps := make(map[string]bool)
	opCalls := make(map[string]map[string]bool)
	for _, svc := range cfg.Services {
		if len(svc.Operations) == 0 {
			return fmt.Errorf("service %q must have at least one operation, e.g.\n  operations:\n    GET /users:\n      duration: 50ms", svc.Name)
		}
		for _, op := range svc.Operations {
			ref := svc.Name + "." + op.Name
			knownOps[ref] = true
			targets := make(map[string]bool, len(op.Calls))
			for _, call := range op.Calls {
				targets[call.Target] = true
			}
			opCalls[ref] = targets
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

			for i, evt := range op.Events {
				if evt.Name == "" {
					return fmt.Errorf("service %q operation %q: event[%d]: name is required", svc.Name, op.Name, i)
				}
				if evt.Delay != "" {
					if _, err := time.ParseDuration(evt.Delay); err != nil {
						return fmt.Errorf("service %q operation %q: event %q: invalid delay: %w", svc.Name, op.Name, evt.Name, err)
					}
				}
				for attrName, attrCfg := range evt.Attributes {
					if _, err := NewAttributeGenerator(attrCfg); err != nil {
						return fmt.Errorf("service %q operation %q: event %q: attribute %q: %w", svc.Name, op.Name, evt.Name, attrName, err)
					}
				}
			}

			if op.QueueDepth < 0 {
				return fmt.Errorf("service %q operation %q: queue_depth must not be negative", svc.Name, op.Name)
			}

			if bp := op.Backpressure; bp != nil {
				if bp.LatencyThreshold == "" {
					return fmt.Errorf("service %q operation %q: backpressure requires latency_threshold", svc.Name, op.Name)
				}
				if _, err := time.ParseDuration(bp.LatencyThreshold); err != nil {
					return fmt.Errorf("service %q operation %q: backpressure: invalid latency_threshold: %w", svc.Name, op.Name, err)
				}
				if bp.DurationMultiplier < 0 {
					return fmt.Errorf("service %q operation %q: backpressure: duration_multiplier must not be negative", svc.Name, op.Name)
				}
				if bp.ErrorRateAdd != "" {
					if _, err := parseErrorRate(bp.ErrorRateAdd); err != nil {
						return fmt.Errorf("service %q operation %q: backpressure: invalid error_rate_add: %w", svc.Name, op.Name, err)
					}
				}
			}

			if cb := op.CircuitBreaker; cb != nil {
				if cb.FailureThreshold <= 0 {
					return fmt.Errorf("service %q operation %q: circuit_breaker: failure_threshold must be positive", svc.Name, op.Name)
				}
				if cb.Window == "" {
					return fmt.Errorf("service %q operation %q: circuit_breaker requires window", svc.Name, op.Name)
				}
				if _, err := time.ParseDuration(cb.Window); err != nil {
					return fmt.Errorf("service %q operation %q: circuit_breaker: invalid window: %w", svc.Name, op.Name, err)
				}
				if cb.Cooldown == "" {
					return fmt.Errorf("service %q operation %q: circuit_breaker requires cooldown", svc.Name, op.Name)
				}
				if _, err := time.ParseDuration(cb.Cooldown); err != nil {
					return fmt.Errorf("service %q operation %q: circuit_breaker: invalid cooldown: %w", svc.Name, op.Name, err)
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
				if call.Timeout != "" {
					d, err := time.ParseDuration(call.Timeout)
					if err != nil {
						return fmt.Errorf("service %q operation %q: call %q invalid timeout: %w", svc.Name, op.Name, call.Target, err)
					}
					if d <= 0 {
						return fmt.Errorf("service %q operation %q: call %q timeout must be positive", svc.Name, op.Name, call.Target)
					}
				}
				if call.Retries < 0 {
					return fmt.Errorf("service %q operation %q: call %q retries must not be negative", svc.Name, op.Name, call.Target)
				}
				if call.RetryBackoff != "" {
					d, err := time.ParseDuration(call.RetryBackoff)
					if err != nil {
						return fmt.Errorf("service %q operation %q: call %q invalid retry_backoff: %w", svc.Name, op.Name, call.Target, err)
					}
					if d < 0 {
						return fmt.Errorf("service %q operation %q: call %q retry_backoff must not be negative", svc.Name, op.Name, call.Target)
					}
				}
				if call.RetryBackoff != "" && call.Retries == 0 {
					return fmt.Errorf("service %q operation %q: call %q retry_backoff requires retries > 0", svc.Name, op.Name, call.Target)
				}
				if call.Async && call.Retries > 0 {
					return fmt.Errorf("service %q operation %q: call %q: async calls cannot have retries", svc.Name, op.Name, call.Target)
				}
				if call.Async && call.Timeout != "" {
					return fmt.Errorf("service %q operation %q: call %q: async calls cannot have a timeout", svc.Name, op.Name, call.Target)
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
			if err := validateCallChanges(sc.Name, ref, override, knownOps, opCalls[ref]); err != nil {
				return err
			}
		}
		if sc.Traffic != nil {
			if err := validateTrafficConfig(*sc.Traffic, false); err != nil {
				return fmt.Errorf("scenario %q: traffic: %w", sc.Name, err)
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

// validateCallChanges validates add_calls and remove_calls within a scenario override.
func validateCallChanges(scenarioName, ref string, override OverrideConfig, knownOps map[string]bool, callerTargets map[string]bool) error {
	prefix := fmt.Sprintf("scenario %q: override %q", scenarioName, ref)
	for _, call := range override.AddCalls {
		if err := validateCallConfig(call, knownOps); err != nil {
			return fmt.Errorf("%s: add_calls: %w", prefix, err)
		}
	}
	for _, rc := range override.RemoveCalls {
		if !strings.Contains(rc.Target, ".") {
			return fmt.Errorf("%s: remove_calls: target %q must be in service.operation format", prefix, rc.Target)
		}
		if !knownOps[rc.Target] {
			return fmt.Errorf("%s: remove_calls: target %q references unknown operation", prefix, rc.Target)
		}
		if !callerTargets[rc.Target] {
			return fmt.Errorf("%s: remove_calls: target %q is not called by %s", prefix, rc.Target, ref)
		}
	}
	return nil
}

// validateCallConfig checks a single CallConfig for structural correctness.
func validateCallConfig(call CallConfig, knownOps map[string]bool) error {
	if !strings.Contains(call.Target, ".") {
		return fmt.Errorf("target %q must be in service.operation format", call.Target)
	}
	if !knownOps[call.Target] {
		return fmt.Errorf("target %q references unknown operation", call.Target)
	}
	if call.Probability < 0 || call.Probability > 1 {
		return fmt.Errorf("target %q probability must be between 0 and 1", call.Target)
	}
	if call.Condition != "" && call.Condition != "on-error" && call.Condition != "on-success" {
		return fmt.Errorf("target %q condition must be \"on-error\" or \"on-success\", got %q", call.Target, call.Condition)
	}
	if call.Count < 0 {
		return fmt.Errorf("target %q count must not be negative", call.Target)
	}
	if call.Timeout != "" {
		d, err := time.ParseDuration(call.Timeout)
		if err != nil {
			return fmt.Errorf("target %q invalid timeout: %w", call.Target, err)
		}
		if d <= 0 {
			return fmt.Errorf("target %q timeout must be positive", call.Target)
		}
	}
	if call.Retries < 0 {
		return fmt.Errorf("target %q retries must not be negative", call.Target)
	}
	if call.RetryBackoff != "" {
		d, err := time.ParseDuration(call.RetryBackoff)
		if err != nil {
			return fmt.Errorf("target %q invalid retry_backoff: %w", call.Target, err)
		}
		if d < 0 {
			return fmt.Errorf("target %q retry_backoff must not be negative", call.Target)
		}
	}
	if call.RetryBackoff != "" && call.Retries == 0 {
		return fmt.Errorf("target %q retry_backoff requires retries > 0", call.Target)
	}
	if call.Async && call.Retries > 0 {
		return fmt.Errorf("target %q: async calls cannot have retries", call.Target)
	}
	if call.Async && call.Timeout != "" {
		return fmt.Errorf("target %q: async calls cannot have a timeout", call.Target)
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
