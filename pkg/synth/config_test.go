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
}

func TestLoadConfig_NewGenerators(t *testing.T) {
	t.Parallel()

	t.Run("probability field", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
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
