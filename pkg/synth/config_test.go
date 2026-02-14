// Tests for YAML DSL configuration loading and validation
// Covers valid configs, invalid configs, and edge cases
package synth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	t.Run("valid minimal config", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        error_rate: 0.1%
traffic:
  rate: 100/s
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.Equal(t, 1, cfg.Version)
		require.Len(t, cfg.Services, 1)
		assert.Equal(t, "gateway", cfg.Services[0].Name)
		require.Len(t, cfg.Services[0].Operations, 1)
		assert.Equal(t, "GET /users", cfg.Services[0].Operations[0].Name)
		assert.Equal(t, "30ms +/- 10ms", cfg.Services[0].Operations[0].Duration)
		assert.Equal(t, "0.1%", cfg.Services[0].Operations[0].ErrorRate)
		assert.Equal(t, "100/s", cfg.Traffic.Rate)
	})

	t.Run("full config with calls and scenarios", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  gateway:
    attributes:
      deployment.environment: production
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        error_rate: 0.1%
        calls:
          - user-service.list
  user-service:
    operations:
      list:
        duration: 20ms +/- 5ms
        error_rate: 0.1%

traffic:
  rate: 100/s
  pattern: uniform

scenarios:
  - name: database degradation
    at: +5m
    duration: 10m
    override:
      user-service.list:
        duration: 500ms +/- 100ms
        error_rate: 15%
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		require.Len(t, cfg.Services, 2)

		// Check calls reference
		var gateway *ServiceConfig
		for i := range cfg.Services {
			if cfg.Services[i].Name == "gateway" {
				gateway = &cfg.Services[i]
			}
		}
		require.NotNil(t, gateway)
		require.Len(t, gateway.Operations, 1)
		assert.Equal(t, []CallConfig{{Target: "user-service.list"}}, gateway.Operations[0].Calls)

		// Check scenario
		require.Len(t, cfg.Scenarios, 1)
		assert.Equal(t, "database degradation", cfg.Scenarios[0].Name)
		assert.Equal(t, "+5m", cfg.Scenarios[0].At)
		assert.Equal(t, "10m", cfg.Scenarios[0].Duration)
		require.Contains(t, cfg.Scenarios[0].Override, "user-service.list")
	})

	t.Run("mixed simple and rich call forms", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        calls:
          - user-service.list
          - target: audit.log
            probability: 0.5
          - target: cache.get
            condition: on-success
            count: 2
  user-service:
    operations:
      list:
        duration: 20ms +/- 5ms
  audit:
    operations:
      log:
        duration: 5ms
  cache:
    operations:
      get:
        duration: 1ms
traffic:
  rate: 100/s
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)

		var gateway *ServiceConfig
		for i := range cfg.Services {
			if cfg.Services[i].Name == "gateway" {
				gateway = &cfg.Services[i]
			}
		}
		require.NotNil(t, gateway)

		calls := gateway.Operations[0].Calls
		require.Len(t, calls, 3)

		assert.Equal(t, "user-service.list", calls[0].Target)
		assert.Equal(t, float64(0), calls[0].Probability)
		assert.Equal(t, "", calls[0].Condition)
		assert.Equal(t, 0, calls[0].Count)

		assert.Equal(t, "audit.log", calls[1].Target)
		assert.InDelta(t, 0.5, calls[1].Probability, 0.001)

		assert.Equal(t, "cache.get", calls[2].Target)
		assert.Equal(t, "on-success", calls[2].Condition)
		assert.Equal(t, 2, calls[2].Count)
	})

	t.Run("config with attributes and call_style", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        call_style: parallel
        attributes:
          http.route:
            value: "/api/v1/users"
          http.response.status_code:
            values:
              "200": 95
              "500": 5
          user.id:
            sequence: "user-{n}"
traffic:
  rate: 100/s
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		require.Len(t, cfg.Services, 1)

		op := cfg.Services[0].Operations[0]
		assert.Equal(t, "parallel", op.CallStyle)
		require.Len(t, op.Attributes, 3)
		assert.Equal(t, "/api/v1/users", op.Attributes["http.route"].Value)
		assert.Equal(t, map[string]int{"200": 95, "500": 5}, op.Attributes["http.response.status_code"].Values)
		assert.Equal(t, "user-{n}", op.Attributes["user.id"].Sequence)
	})

	t.Run("domain field parsed from YAML", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  gateway:
    operations:
      GET /users:
        domain: http
        duration: 30ms +/- 10ms
        attributes:
          http.route:
            value: "/api/v1/users"
traffic:
  rate: 100/s
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		require.Len(t, cfg.Services, 1)
		op := cfg.Services[0].Operations[0]
		assert.Equal(t, "http", op.Domain)
		assert.Equal(t, "/api/v1/users", op.Attributes["http.route"].Value)
	})

	t.Run("domain field optional", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
traffic:
  rate: 100/s
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		op := cfg.Services[0].Operations[0]
		assert.Empty(t, op.Domain)
	})

	t.Run("file not found", func(t *testing.T) {
		t.Parallel()
		_, err := LoadConfig("/nonexistent/path.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading config")
	})

	t.Run("missing version", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
traffic:
  rate: 100/s
`)
		_, err := LoadConfig(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing required field: version")
	})

	t.Run("explicit version zero", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 0
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
traffic:
  rate: 100/s
`)
		_, err := LoadConfig(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported config version 0")
	})

	t.Run("unsupported version", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 99
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
traffic:
  rate: 100/s
`)
		_, err := LoadConfig(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported config version 99")
	})

	t.Run("invalid YAML", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `{{{invalid yaml`)
		_, err := LoadConfig(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing config")
	})
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	t.Run("no services", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one service")
	})

	t.Run("no operations", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{Name: "svc"}},
			Traffic:  TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one operation")
	})

	t.Run("invalid duration", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "not-a-duration",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duration")
	})

	t.Run("invalid error rate", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:      "op",
					Duration:  "10ms",
					ErrorRate: "abc",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error_rate")
	})

	t.Run("invalid traffic rate", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "bad"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "traffic rate")
	})

	t.Run("broken call reference", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "nonexistent.op"}},
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nonexistent.op")
	})

	t.Run("invalid call reference format", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "no-dot-separator"}},
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "service.operation")
	})

	t.Run("valid config passes", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "gateway",
					Operations: []OperationConfig{{
						Name:     "GET /users",
						Duration: "30ms +/- 10ms",
						Calls:    []CallConfig{{Target: "backend.list"}},
					}},
				},
				{
					Name: "backend",
					Operations: []OperationConfig{{
						Name:      "list",
						Duration:  "20ms +/- 5ms",
						ErrorRate: "0.1%",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.NoError(t, err)
	})

	t.Run("scenario override references valid operation", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
			Scenarios: []ScenarioConfig{{
				Name:     "test",
				At:       "+1m",
				Duration: "5m",
				Override: map[string]OverrideConfig{
					"svc.op": {Duration: "100ms"},
				},
			}},
		}
		err := ValidateConfig(cfg)
		require.NoError(t, err)
	})

	t.Run("scenario with invalid at", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
			Scenarios: []ScenarioConfig{{
				Name:     "test",
				At:       "garbage",
				Duration: "5m",
				Override: map[string]OverrideConfig{
					"svc.op": {Duration: "100ms"},
				},
			}},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "scenario \"test\"")
		assert.Contains(t, err.Error(), "invalid at")
	})

	t.Run("scenario with invalid duration", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
			Scenarios: []ScenarioConfig{{
				Name:     "test",
				At:       "+1m",
				Duration: "not-a-duration",
				Override: map[string]OverrideConfig{
					"svc.op": {Duration: "100ms"},
				},
			}},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "scenario \"test\"")
		assert.Contains(t, err.Error(), "invalid duration")
	})

	t.Run("valid call_style sequential", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:      "op",
					Duration:  "10ms",
					CallStyle: "sequential",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.NoError(t, err)
	})

	t.Run("valid call_style parallel", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:      "op",
					Duration:  "10ms",
					CallStyle: "parallel",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.NoError(t, err)
	})

	t.Run("invalid call_style", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:      "op",
					Duration:  "10ms",
					CallStyle: "batched",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "call_style")
	})

	t.Run("valid operation attributes", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
					Attributes: map[string]AttributeValueConfig{
						"http.route":                {Value: "/api/v1/users"},
						"http.response.status_code": {Values: map[string]int{"200": 95, "500": 5}},
						"user.id":                   {Sequence: "user-{n}"},
					},
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.NoError(t, err)
	})

	t.Run("invalid operation attribute config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
					Attributes: map[string]AttributeValueConfig{
						"bad": {},
					},
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attribute")
	})

	t.Run("invalid call probability", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "svc",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
						Calls:    []CallConfig{{Target: "other.op", Probability: 1.5}},
					}},
				},
				{
					Name: "other",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probability")
	})

	t.Run("negative call probability", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "svc",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
						Calls:    []CallConfig{{Target: "other.op", Probability: -0.1}},
					}},
				},
				{
					Name: "other",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probability")
	})

	t.Run("invalid call condition", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "svc",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
						Calls:    []CallConfig{{Target: "other.op", Condition: "on-timeout"}},
					}},
				},
				{
					Name: "other",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "condition")
	})

	t.Run("negative call count", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "svc",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
						Calls:    []CallConfig{{Target: "other.op", Count: -1}},
					}},
				},
				{
					Name: "other",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "count")
	})

	t.Run("valid rich call config passes", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "svc",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
						Calls: []CallConfig{
							{Target: "other.op", Probability: 0.5, Condition: "on-error", Count: 3},
						},
					}},
				},
				{
					Name: "other",
					Operations: []OperationConfig{{
						Name:     "op",
						Duration: "10ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		err := ValidateConfig(cfg)
		require.NoError(t, err)
	})

	t.Run("scenario override references invalid operation", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
			Scenarios: []ScenarioConfig{{
				Name:     "test",
				At:       "+1m",
				Duration: "5m",
				Override: map[string]OverrideConfig{
					"nonexistent.op": {Duration: "100ms"},
				},
			}},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nonexistent.op")
	})

	t.Run("scenario override with valid attributes", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Scenarios = []ScenarioConfig{{
			Name:     "test",
			At:       "+1m",
			Duration: "5m",
			Override: map[string]OverrideConfig{
				"svc.op": {
					Attributes: map[string]AttributeValueConfig{
						"status": {Values: map[string]int{"503": 80, "200": 20}},
					},
				},
			},
		}}
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("scenario override with invalid attribute", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Scenarios = []ScenarioConfig{{
			Name:     "test",
			At:       "+1m",
			Duration: "5m",
			Override: map[string]OverrideConfig{
				"svc.op": {
					Attributes: map[string]AttributeValueConfig{
						"bad": {Range: []int64{5, 3, 1}},
					},
				},
			},
		}}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attribute")
	})

	t.Run("scenario with valid traffic override", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Scenarios = []ScenarioConfig{{
			Name:     "spike",
			At:       "+1m",
			Duration: "5m",
			Traffic:  &TrafficConfig{Rate: "500/s"},
		}}
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("scenario with invalid traffic override", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Scenarios = []ScenarioConfig{{
			Name:     "bad",
			At:       "+1m",
			Duration: "5m",
			Traffic:  &TrafficConfig{Rate: "not-a-rate"},
		}}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "traffic")
	})

	t.Run("scenario with traffic only and no overrides", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Scenarios = []ScenarioConfig{{
			Name:     "rate-only",
			At:       "+1m",
			Duration: "5m",
			Traffic:  &TrafficConfig{Rate: "200/s"},
		}}
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("bursty fields valid", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "bursty"
		cfg.Traffic.BurstMultiplier = 3
		cfg.Traffic.BurstInterval = "2m"
		cfg.Traffic.BurstDuration = "15s"
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("bursty fields with invalid burst_interval", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "bursty"
		cfg.Traffic.BurstInterval = "bad"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_interval")
	})

	t.Run("bursty fields with invalid burst_duration", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "bursty"
		cfg.Traffic.BurstDuration = "bad"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst_duration")
	})

	t.Run("bursty fields rejected on non-bursty pattern", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "uniform"
		cfg.Traffic.BurstMultiplier = 3
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "burst")
	})

	t.Run("diurnal fields valid", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "diurnal"
		cfg.Traffic.PeakMultiplier = 2.0
		cfg.Traffic.TroughMultiplier = 0.2
		cfg.Traffic.Period = "12h"
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("diurnal fields with invalid period", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "diurnal"
		cfg.Traffic.Period = "bad"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "period")
	})

	t.Run("diurnal fields rejected on non-diurnal pattern", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "uniform"
		cfg.Traffic.PeakMultiplier = 2.0
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "peak_multiplier")
	})

	t.Run("segments valid for custom pattern", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "custom"
		cfg.Traffic.Segments = []SegmentConfig{
			{Until: "5m", Rate: "50/s"},
			{Until: "10m", Rate: "200/s"},
		}
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("segments with invalid until", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "custom"
		cfg.Traffic.Segments = []SegmentConfig{
			{Until: "bad", Rate: "50/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "until")
	})

	t.Run("segments with invalid rate", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "custom"
		cfg.Traffic.Segments = []SegmentConfig{
			{Until: "5m", Rate: "bad"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rate")
	})

	t.Run("segments rejected on non-custom pattern", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "uniform"
		cfg.Traffic.Segments = []SegmentConfig{
			{Until: "5m", Rate: "50/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "segments")
	})

	t.Run("custom pattern requires segments", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "custom"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "segments")
	})

	t.Run("overlay validated recursively", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Pattern = "diurnal"
		cfg.Traffic.Overlay = &TrafficConfig{
			Rate:            "100/s",
			Pattern:         "bursty",
			BurstMultiplier: 3,
			BurstInterval:   "2m",
			BurstDuration:   "15s",
		}
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("overlay with invalid config", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Overlay = &TrafficConfig{
			Rate:          "100/s",
			Pattern:       "bursty",
			BurstInterval: "bad",
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overlay")
	})

	t.Run("nested overlay rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Traffic.Overlay = &TrafficConfig{
			Rate:    "100/s",
			Pattern: "uniform",
			Overlay: &TrafficConfig{
				Rate:    "100/s",
				Pattern: "bursty",
			},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nested")
	})

	t.Run("valid call timeout", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Timeout = "100ms"
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("invalid call timeout", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Timeout = "not-a-duration"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timeout")
	})

	t.Run("zero call timeout rejected", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Timeout = "0s"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timeout must be positive")
	})

	t.Run("negative call timeout rejected", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Timeout = "-5ms"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timeout must be positive")
	})

	t.Run("valid retries", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Retries = 2
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("negative retries rejected", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Retries = -1
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "retries")
	})

	t.Run("valid retry_backoff with retries", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Retries = 2
		cfg.Services[0].Operations[0].Calls[0].RetryBackoff = "50ms"
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("invalid retry_backoff", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Retries = 2
		cfg.Services[0].Operations[0].Calls[0].RetryBackoff = "bad"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "retry_backoff")
	})

	t.Run("negative retry_backoff rejected", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].Retries = 2
		cfg.Services[0].Operations[0].Calls[0].RetryBackoff = "-10ms"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "retry_backoff must not be negative")
	})

	t.Run("retry_backoff without retries rejected", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		cfg.Services[0].Operations[0].Calls[0].RetryBackoff = "50ms"
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "retry_backoff requires retries > 0")
	})

	t.Run("call without timeout or retries unchanged", func(t *testing.T) {
		t.Parallel()
		cfg := twoServiceConfig()
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("negative queue_depth rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].QueueDepth = -1
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "queue_depth must not be negative")
	})

	t.Run("zero queue_depth accepted", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].QueueDepth = 0
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("positive queue_depth accepted", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].QueueDepth = 10
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("backpressure missing latency_threshold rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].Backpressure = &BackpressureConfig{
			DurationMultiplier: 2.0,
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backpressure requires latency_threshold")
	})

	t.Run("backpressure invalid latency_threshold rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].Backpressure = &BackpressureConfig{
			LatencyThreshold: "not-a-duration",
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid latency_threshold")
	})

	t.Run("backpressure negative duration_multiplier rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].Backpressure = &BackpressureConfig{
			LatencyThreshold:   "50ms",
			DurationMultiplier: -1.0,
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duration_multiplier must not be negative")
	})

	t.Run("backpressure invalid error_rate_add rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].Backpressure = &BackpressureConfig{
			LatencyThreshold: "50ms",
			ErrorRateAdd:     "garbage",
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid error_rate_add")
	})

	t.Run("valid backpressure accepted", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].Backpressure = &BackpressureConfig{
			LatencyThreshold:   "100ms",
			DurationMultiplier: 3.0,
			ErrorRateAdd:       "10%",
		}
		require.NoError(t, ValidateConfig(cfg))
	})

	t.Run("circuit_breaker missing failure_threshold rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].CircuitBreaker = &CircuitBreakerConfig{
			Window:   "1m",
			Cooldown: "10s",
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failure_threshold must be positive")
	})

	t.Run("circuit_breaker missing window rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].CircuitBreaker = &CircuitBreakerConfig{
			FailureThreshold: 5,
			Cooldown:         "10s",
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "circuit_breaker requires window")
	})

	t.Run("circuit_breaker invalid window rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].CircuitBreaker = &CircuitBreakerConfig{
			FailureThreshold: 5,
			Window:           "bad",
			Cooldown:         "10s",
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid window")
	})

	t.Run("circuit_breaker missing cooldown rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].CircuitBreaker = &CircuitBreakerConfig{
			FailureThreshold: 5,
			Window:           "1m",
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "circuit_breaker requires cooldown")
	})

	t.Run("circuit_breaker invalid cooldown rejected", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].CircuitBreaker = &CircuitBreakerConfig{
			FailureThreshold: 5,
			Window:           "1m",
			Cooldown:         "not-a-duration",
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid cooldown")
	})

	t.Run("valid circuit_breaker accepted", func(t *testing.T) {
		t.Parallel()
		cfg := validBaseConfig()
		cfg.Services[0].Operations[0].CircuitBreaker = &CircuitBreakerConfig{
			FailureThreshold: 5,
			Window:           "1m",
			Cooldown:         "30s",
		}
		require.NoError(t, ValidateConfig(cfg))
	})
}

func twoServiceConfig() *Config {
	return &Config{
		Services: []ServiceConfig{
			{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "other.op"}},
				}},
			},
			{
				Name: "other",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "5ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}
}

func validBaseConfig() *Config {
	return &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "10ms",
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}
}

func TestLoadConfigCallTimeout(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, `
version: 1
services:
  gateway:
    operations:
      request:
        duration: 10ms
        calls:
          - target: backend.query
            timeout: 100ms
            retries: 2
            retry_backoff: 50ms
  backend:
    operations:
      query:
        duration: 20ms
traffic:
  rate: 100/s
`)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	calls := cfg.Services[0].Operations[0].Calls
	if cfg.Services[0].Name != "gateway" {
		calls = cfg.Services[1].Operations[0].Calls
	}
	require.Len(t, calls, 1)
	assert.Equal(t, "100ms", calls[0].Timeout)
	assert.Equal(t, 2, calls[0].Retries)
	assert.Equal(t, "50ms", calls[0].RetryBackoff)

	require.NoError(t, ValidateConfig(cfg))
}

func TestLoadConfig_NewGenerators(t *testing.T) {
	t.Parallel()

	t.Run("probability field", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        attributes:
          cache.hit:
            probability: 0.85
traffic:
  rate: 100/s
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		op := cfg.Services[0].Operations[0]
		require.NotNil(t, op.Attributes["cache.hit"].Probability)
		assert.InDelta(t, 0.85, *op.Attributes["cache.hit"].Probability, 0.001)
	})

	t.Run("range field", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        attributes:
          http.response.status_code:
            range: [200, 599]
traffic:
  rate: 100/s
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		op := cfg.Services[0].Operations[0]
		assert.Equal(t, []int64{200, 599}, op.Attributes["http.response.status_code"].Range)
	})

	t.Run("distribution field", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        attributes:
          http.response.body.size:
            distribution:
              mean: 4096
              stddev: 1024
traffic:
  rate: 100/s
`)
		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		op := cfg.Services[0].Operations[0]
		require.NotNil(t, op.Attributes["http.response.body.size"].Distribution)
		assert.InDelta(t, 4096, op.Attributes["http.response.body.size"].Distribution.Mean, 0.001)
		assert.InDelta(t, 1024, op.Attributes["http.response.body.size"].Distribution.StdDev, 0.001)
	})
}
