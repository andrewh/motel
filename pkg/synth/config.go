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

// reservedResourceAttribute lists OTel resource keys that motel sets automatically.
// Users must not override these in resource_attributes.
var reservedResourceAttribute = map[string]bool{
	"service.name":  true,
	"motel.version": true,
}

// Metric type constants for OTel instrument types.
const (
	metricTypeCounter       = "counter"
	metricTypeUpDownCounter = "updowncounter"
	metricTypeHistogram     = "histogram"
	metricTypeGauge         = "gauge"
)

// validMetricType lists supported OTel instrument types.
var validMetricType = map[string]bool{
	metricTypeCounter:       true,
	metricTypeUpDownCounter: true,
	metricTypeHistogram:     true,
	metricTypeGauge:         true,
}

// Log severity constants matching the OTel log data model severity text values.
const (
	logSeverityTrace = "TRACE"
	logSeverityDebug = "DEBUG"
	logSeverityInfo  = "INFO"
	logSeverityWarn  = "WARN"
	logSeverityError = "ERROR"
	logSeverityFatal = "FATAL"
)

// validLogSeverity lists supported log severity levels. It is derived from
// severityByName so the validation set cannot drift from the severities the
// LogObserver can emit.
var validLogSeverity = func() map[string]bool {
	m := make(map[string]bool, len(severityByName))
	for name := range severityByName {
		m[name] = true
	}
	return m
}()

// Log condition constants controlling when a topology log record is emitted.
const (
	logConditionError   = "error"
	logConditionSuccess = "success"
	logConditionSlow    = "slow"
)

// Log timing anchor constants for the at: field.
const (
	logAtStart = "start"
	logAtEnd   = "end"
)

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
	ResourceAttributes map[string]string             `yaml:"resource_attributes,omitempty"`
	Attributes         map[string]string             `yaml:"attributes,omitempty"`
	Metrics            []MetricConfig                `yaml:"metrics,omitempty"`
	Logs               []LogConfig                   `yaml:"logs,omitempty"`
	Operations         map[string]rawOperationConfig `yaml:"operations"`
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

// LogConfig describes a log record template defined in the topology YAML.
type LogConfig struct {
	Severity    string                          `yaml:"severity"`
	Body        string                          `yaml:"body"`
	Condition   string                          `yaml:"condition,omitempty"`
	Probability *float64                        `yaml:"probability,omitempty"`
	At          string                          `yaml:"at,omitempty"`
	Delay       string                          `yaml:"delay,omitempty"`
	Attributes  map[string]AttributeValueConfig `yaml:"attributes,omitempty"`
}

// MetricConfig describes a metric instrument defined in the topology YAML.
type MetricConfig struct {
	Name       string                          `yaml:"name"`
	Type       string                          `yaml:"type"`
	Unit       string                          `yaml:"unit,omitempty"`
	Value      string                          `yaml:"value,omitempty"`
	Interval   string                          `yaml:"interval,omitempty"`
	Walk       string                          `yaml:"walk,omitempty"`
	Min        *float64                        `yaml:"min,omitempty"`
	Max        *float64                        `yaml:"max,omitempty"`
	ErrorsOnly bool                            `yaml:"errors_only,omitempty"`
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
	Links          []string                        `yaml:"links,omitempty"`
	Metrics        []MetricConfig                  `yaml:"metrics,omitempty"`
	Logs           []LogConfig                     `yaml:"logs,omitempty"`
	QueueDepth     int                             `yaml:"queue_depth,omitempty"`
	Backpressure   *BackpressureConfig             `yaml:"backpressure,omitempty"`
	CircuitBreaker *CircuitBreakerConfig           `yaml:"circuit_breaker,omitempty"`
}

// ServiceConfig describes a service in the topology.
type ServiceConfig struct {
	Name               string
	ResourceAttributes map[string]string
	Attributes         map[string]string
	Metrics            []MetricConfig
	Logs               []LogConfig
	Operations         []OperationConfig
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
	Links          []string
	Metrics        []MetricConfig
	Logs           []LogConfig
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

// OverrideConfig holds per-operation or per-service overrides within a scenario.
// Override keys are usually "service.operation" references; a bare service name
// is also accepted, in which case only Metrics may be set.
type OverrideConfig struct {
	Duration    string                          `yaml:"duration,omitempty"`
	ErrorRate   string                          `yaml:"error_rate,omitempty"`
	Attributes  map[string]AttributeValueConfig `yaml:"attributes,omitempty"`
	AddCalls    []CallConfig                    `yaml:"add_calls,omitempty"`
	RemoveCalls []RemoveCallConfig              `yaml:"remove_calls,omitempty"`
	Metrics     map[string]MetricOverrideConfig `yaml:"metrics,omitempty"`
	Logs        *LogOverrideConfig              `yaml:"logs,omitempty"`
}

// LogOverrideConfig modifies topology log output during a scenario window.
// Add defines extra log records emitted only while the scenario is active;
// Disable mutes the base log definitions (topology templates and derived
// error/slow logs) at the override's scope for the duration of the window.
type LogOverrideConfig struct {
	Add     []LogConfig `yaml:"add,omitempty"`
	Disable bool        `yaml:"disable,omitempty"`
}

// MetricOverrideConfig overrides the value distribution of a named metric
// during a scenario window. Name, type, and unit are fixed at instrument
// creation time and cannot be overridden.
type MetricOverrideConfig struct {
	Value string `yaml:"value"`
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
			Name:               name,
			ResourceAttributes: rawSvc.ResourceAttributes,
			Attributes:         rawSvc.Attributes,
			Metrics:            rawSvc.Metrics,
			Logs:               rawSvc.Logs,
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
				Links:          rawOp.Links,
				Metrics:        rawOp.Metrics,
				Logs:           rawOp.Logs,
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
	// knownServices: all defined services (for service-scope metric overrides)
	// opCalls: which targets each operation calls (for remove_calls validation)
	// metricsByScope: metric definitions keyed by scope ref (service name or "service.operation")
	knownOps := make(map[string]bool)
	knownServices := make(map[string]bool)
	opCalls := make(map[string]map[string]bool)
	metricsByScope := make(map[string]map[string]MetricConfig)
	for _, svc := range cfg.Services {
		if len(svc.Operations) == 0 {
			return fmt.Errorf("service %q must have at least one operation, e.g.\n  operations:\n    GET /users:\n      duration: 50ms", svc.Name)
		}
		for k := range svc.ResourceAttributes {
			if k == "" {
				return fmt.Errorf("service %q: resource_attributes key must not be empty", svc.Name)
			}
			if reservedResourceAttribute[k] {
				return fmt.Errorf("service %q: resource_attributes must not contain reserved key %q (set automatically)", svc.Name, k)
			}
		}
		knownServices[svc.Name] = true
		metricNames := make(map[string]bool)
		for i, mc := range svc.Metrics {
			if err := validateMetricConfig(mc, fmt.Sprintf("service %q: metric[%d]", svc.Name, i)); err != nil {
				return err
			}
			if metricNames[mc.Name] {
				return fmt.Errorf("service %q: duplicate metric name %q", svc.Name, mc.Name)
			}
			metricNames[mc.Name] = true
			if metricsByScope[svc.Name] == nil {
				metricsByScope[svc.Name] = make(map[string]MetricConfig)
			}
			metricsByScope[svc.Name][mc.Name] = mc
		}
		for i, lc := range svc.Logs {
			if err := validateLogConfig(lc, fmt.Sprintf("service %q: log[%d]", svc.Name, i)); err != nil {
				return err
			}
		}
		for _, op := range svc.Operations {
			opRef := svc.Name + "." + op.Name
			for i, lc := range op.Logs {
				if err := validateLogConfig(lc, fmt.Sprintf("service %q operation %q: log[%d]", svc.Name, op.Name, i)); err != nil {
					return err
				}
			}
			for i, mc := range op.Metrics {
				if err := validateMetricConfig(mc, fmt.Sprintf("service %q operation %q: metric[%d]", svc.Name, op.Name, i)); err != nil {
					return err
				}
				if metricNames[mc.Name] {
					return fmt.Errorf("service %q operation %q: duplicate metric name %q (already defined at service or operation level)", svc.Name, op.Name, mc.Name)
				}
				metricNames[mc.Name] = true
				if metricsByScope[opRef] == nil {
					metricsByScope[opRef] = make(map[string]MetricConfig)
				}
				metricsByScope[opRef][mc.Name] = mc
			}
			ref := opRef
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
					d, err := time.ParseDuration(evt.Delay)
					if err != nil {
						return fmt.Errorf("service %q operation %q: event %q: invalid delay: %w", svc.Name, op.Name, evt.Name, err)
					}
					if d < 0 {
						return fmt.Errorf("service %q operation %q: event %q: delay must not be negative", svc.Name, op.Name, evt.Name)
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

			ref := svc.Name + "." + op.Name
			seenLinks := make(map[string]bool, len(op.Links))
			for _, link := range op.Links {
				if !strings.Contains(link, ".") {
					return fmt.Errorf("service %q operation %q: link %q must be in service.operation format", svc.Name, op.Name, link)
				}
				if !knownOps[link] {
					return fmt.Errorf("service %q operation %q: link %q references unknown operation", svc.Name, op.Name, link)
				}
				if link == ref {
					return fmt.Errorf("service %q operation %q: link must not reference itself", svc.Name, op.Name)
				}
				if seenLinks[link] {
					return fmt.Errorf("service %q operation %q: duplicate link %q", svc.Name, op.Name, link)
				}
				seenLinks[link] = true
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
		if dur, err := time.ParseDuration(sc.Duration); err != nil {
			return fmt.Errorf("scenario %q: invalid duration: %w", sc.Name, err)
		} else if dur <= 0 {
			return fmt.Errorf("scenario %q: duration must be positive, got %q", sc.Name, sc.Duration)
		}
		for ref, override := range sc.Override {
			if !knownOps[ref] {
				if !knownServices[ref] {
					return fmt.Errorf("scenario %q: override %q references unknown operation or service", sc.Name, ref)
				}
				if override.Duration != "" || override.ErrorRate != "" || len(override.Attributes) > 0 ||
					len(override.AddCalls) > 0 || len(override.RemoveCalls) > 0 {
					return fmt.Errorf("scenario %q: override %q: service-level overrides support only metrics and logs (use %s.<operation> for operation overrides)", sc.Name, ref, ref)
				}
			}
			if err := validateMetricOverrides(sc.Name, ref, override.Metrics, metricsByScope[ref]); err != nil {
				return err
			}
			if err := validateLogOverrides(sc.Name, ref, override.Logs); err != nil {
				return err
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

// validateMetricOverrides checks scenario metric overrides against the metrics
// defined at the override's scope (a service or operation).
func validateMetricOverrides(scenarioName, ref string, overrides map[string]MetricOverrideConfig, defined map[string]MetricConfig) error {
	for name, mo := range overrides {
		base, ok := defined[name]
		if !ok {
			return fmt.Errorf("scenario %q: override %q: metric %q is not defined at this scope", scenarioName, ref, name)
		}
		if base.Value == "" {
			return fmt.Errorf("scenario %q: override %q: metric %q is span-derived and has no value distribution to override", scenarioName, ref, name)
		}
		if mo.Value == "" {
			return fmt.Errorf("scenario %q: override %q: metric %q: value is required", scenarioName, ref, name)
		}
		if _, err := ParseFloatDistribution(mo.Value); err != nil {
			return fmt.Errorf("scenario %q: override %q: metric %q: invalid value: %w", scenarioName, ref, name, err)
		}
	}
	return nil
}

// validateMetricConfig checks a single MetricConfig for structural correctness.
func validateMetricConfig(mc MetricConfig, prefix string) error {
	if mc.Name == "" {
		return fmt.Errorf("%s: name is required", prefix)
	}
	if !validMetricType[mc.Type] {
		return fmt.Errorf("%s %q: type must be one of counter, updowncounter, histogram, gauge; got %q", prefix, mc.Name, mc.Type)
	}
	if mc.Value != "" {
		if _, err := ParseFloatDistribution(mc.Value); err != nil {
			return fmt.Errorf("%s %q: invalid value: %w", prefix, mc.Name, err)
		}
	}
	if mc.Type == metricTypeGauge && mc.Value == "" {
		return fmt.Errorf("%s %q: gauge metrics require a value (gauges are point-in-time, not span-derived)", prefix, mc.Name)
	}
	if mc.Interval != "" {
		if mc.Type == metricTypeGauge {
			return fmt.Errorf("%s %q: interval is not valid for gauge metrics (gauges already emit on the collection cycle)", prefix, mc.Name)
		}
		if mc.Value == "" {
			return fmt.Errorf("%s %q: interval requires a value (span-derived metrics are emitted per span)", prefix, mc.Name)
		}
		if mc.ErrorsOnly {
			return fmt.Errorf("%s %q: interval cannot be combined with errors_only", prefix, mc.Name)
		}
		d, err := time.ParseDuration(mc.Interval)
		if err != nil {
			return fmt.Errorf("%s %q: invalid interval: %w", prefix, mc.Name, err)
		}
		if d <= 0 {
			return fmt.Errorf("%s %q: interval must be positive", prefix, mc.Name)
		}
	}
	if mc.Walk != "" {
		if mc.Type != metricTypeGauge {
			return fmt.Errorf("%s %q: walk is only valid for gauge metrics", prefix, mc.Name)
		}
		d, err := time.ParseDuration(mc.Walk)
		if err != nil {
			return fmt.Errorf("%s %q: invalid walk: %w", prefix, mc.Name, err)
		}
		if d <= 0 {
			return fmt.Errorf("%s %q: walk must be positive", prefix, mc.Name)
		}
	}
	if (mc.Min != nil || mc.Max != nil) && mc.Type != metricTypeGauge {
		return fmt.Errorf("%s %q: min and max bounds are only valid for gauge metrics", prefix, mc.Name)
	}
	if mc.Min != nil && mc.Max != nil && *mc.Min >= *mc.Max {
		return fmt.Errorf("%s %q: min must be less than max", prefix, mc.Name)
	}
	if mc.ErrorsOnly && mc.Type != metricTypeCounter {
		return fmt.Errorf("%s %q: errors_only is only valid for counter metrics", prefix, mc.Name)
	}
	if mc.Type == metricTypeUpDownCounter && mc.Value == "" {
		// Span-derived updowncounters record +1 at span start and -1 at span end.
		// The two observations use independently generated attribute values, so
		// non-static generators produce mismatched time series that never balance.
		for attrName, attrCfg := range mc.Attributes {
			if !IsStaticAttributeConfig(attrCfg) {
				return fmt.Errorf("%s %q: span-derived updowncounter attribute %q must be a static value (random attributes cause +1/-1 to land on different time series)", prefix, mc.Name, attrName)
			}
		}
	}
	for attrName, attrCfg := range mc.Attributes {
		if _, err := NewAttributeGenerator(attrCfg); err != nil {
			return fmt.Errorf("%s %q: attribute %q: %w", prefix, mc.Name, attrName, err)
		}
	}
	return nil
}

// validateLogOverrides checks a scenario log override for structural correctness.
func validateLogOverrides(scenarioName, ref string, lo *LogOverrideConfig) error {
	if lo == nil {
		return nil
	}
	if len(lo.Add) == 0 && !lo.Disable {
		return fmt.Errorf("scenario %q: override %q: logs override must set add or disable", scenarioName, ref)
	}
	for i, lc := range lo.Add {
		prefix := fmt.Sprintf("scenario %q: override %q: logs: add[%d]", scenarioName, ref, i)
		if err := validateLogConfig(lc, prefix); err != nil {
			return err
		}
	}
	return nil
}

// validateLogConfig checks a single LogConfig for structural correctness.
func validateLogConfig(lc LogConfig, prefix string) error {
	if lc.Severity == "" {
		return fmt.Errorf("%s: severity is required (one of TRACE, DEBUG, INFO, WARN, ERROR, FATAL)", prefix)
	}
	if !validLogSeverity[strings.ToUpper(lc.Severity)] {
		return fmt.Errorf("%s: severity must be one of TRACE, DEBUG, INFO, WARN, ERROR, FATAL; got %q", prefix, lc.Severity)
	}
	if lc.Body == "" {
		return fmt.Errorf("%s: body is required", prefix)
	}
	switch lc.Condition {
	case "", logConditionError, logConditionSuccess, logConditionSlow:
	default:
		return fmt.Errorf("%s: condition must be %q, %q, or %q; got %q", prefix, logConditionError, logConditionSuccess, logConditionSlow, lc.Condition)
	}
	if lc.Probability != nil && (*lc.Probability < 0 || *lc.Probability > 1) {
		return fmt.Errorf("%s: probability must be between 0 and 1, got %v", prefix, *lc.Probability)
	}
	switch lc.At {
	case "", logAtStart, logAtEnd:
	default:
		return fmt.Errorf("%s: at must be %q or %q; got %q", prefix, logAtStart, logAtEnd, lc.At)
	}
	if lc.Delay != "" {
		d, err := time.ParseDuration(lc.Delay)
		if err != nil {
			return fmt.Errorf("%s: invalid delay: %w", prefix, err)
		}
		if d < 0 {
			return fmt.Errorf("%s: delay must not be negative", prefix)
		}
	}
	for attrName, attrCfg := range lc.Attributes {
		if _, err := NewAttributeGenerator(attrCfg); err != nil {
			return fmt.Errorf("%s: attribute %q: %w", prefix, attrName, err)
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
