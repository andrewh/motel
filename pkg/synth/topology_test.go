// Tests for topology graph construction from config
// Validates reference resolution, root detection, and cycle detection
package synth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTopology(t *testing.T) {
	t.Parallel()

	t.Run("simple two-service graph", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "gateway",
					Operations: []OperationConfig{{
						Name:     "GET /users",
						Duration: "30ms +/- 10ms",
						Calls:    []string{"backend.list"},
					}},
				},
				{
					Name: "backend",
					Operations: []OperationConfig{{
						Name:     "list",
						Duration: "20ms +/- 5ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}

		topo, err := BuildTopology(cfg)
		require.NoError(t, err)

		require.Len(t, topo.Services, 2)
		assert.Contains(t, topo.Services, "gateway")
		assert.Contains(t, topo.Services, "backend")

		// gateway.GET /users should call backend.list
		gatewayOp := topo.Services["gateway"].Operations["GET /users"]
		require.Len(t, gatewayOp.Calls, 1)
		assert.Equal(t, "list", gatewayOp.Calls[0].Name)
		assert.Equal(t, "backend", gatewayOp.Calls[0].Service.Name)

		// backend.list is a leaf
		backendOp := topo.Services["backend"].Operations["list"]
		assert.Empty(t, backendOp.Calls)
	})

	t.Run("detects roots automatically", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "a",
					Operations: []OperationConfig{{
						Name:     "entry",
						Duration: "10ms",
						Calls:    []string{"b.work"},
					}},
				},
				{
					Name: "b",
					Operations: []OperationConfig{{
						Name:     "work",
						Duration: "5ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg)
		require.NoError(t, err)
		require.Len(t, topo.Roots, 1)
		assert.Equal(t, "entry", topo.Roots[0].Name)
		assert.Equal(t, "a", topo.Roots[0].Service.Name)
	})

	t.Run("multiple roots", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "gateway",
					Operations: []OperationConfig{
						{Name: "GET /users", Duration: "10ms", Calls: []string{"backend.list"}},
						{Name: "POST /orders", Duration: "20ms", Calls: []string{"backend.create"}},
					},
				},
				{
					Name: "backend",
					Operations: []OperationConfig{
						{Name: "list", Duration: "5ms"},
						{Name: "create", Duration: "15ms"},
					},
				},
			},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg)
		require.NoError(t, err)
		assert.Len(t, topo.Roots, 2)
	})

	t.Run("cycle detection", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "a",
					Operations: []OperationConfig{{
						Name:     "op1",
						Duration: "10ms",
						Calls:    []string{"b.op2"},
					}},
				},
				{
					Name: "b",
					Operations: []OperationConfig{{
						Name:     "op2",
						Duration: "10ms",
						Calls:    []string{"a.op1"},
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		_, err := BuildTopology(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cycle")
	})

	t.Run("preserves service attributes", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name:       "svc",
				Attributes: map[string]string{"env": "prod"},
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg)
		require.NoError(t, err)
		assert.Equal(t, "prod", topo.Services["svc"].Attributes["env"])
	})

	t.Run("resolves operation attributes", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
					Attributes: map[string]AttributeValueConfig{
						"http.route": {Value: "/api/v1/users"},
						"status":     {Values: map[string]int{"200": 1}},
						"req.id":     {Sequence: "req-{n}"},
					},
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg)
		require.NoError(t, err)
		op := topo.Services["svc"].Operations["op"]
		require.Len(t, op.Attributes, 3)
		assert.IsType(t, &StaticValue{}, op.Attributes["http.route"])
		assert.IsType(t, &WeightedChoice{}, op.Attributes["status"])
		assert.IsType(t, &SequenceValue{}, op.Attributes["req.id"])
	})

	t.Run("resolves call_style", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{
					{Name: "parallel-op", Duration: "10ms", CallStyle: "parallel"},
					{Name: "sequential-op", Duration: "10ms", CallStyle: "sequential"},
					{Name: "default-op", Duration: "10ms"},
				},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg)
		require.NoError(t, err)
		assert.Equal(t, "parallel", topo.Services["svc"].Operations["parallel-op"].CallStyle)
		assert.Equal(t, "sequential", topo.Services["svc"].Operations["sequential-op"].CallStyle)
		assert.Equal(t, "", topo.Services["svc"].Operations["default-op"].CallStyle)
	})

	t.Run("preserves error rate", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:      "op",
					Duration:  "10ms",
					ErrorRate: "5%",
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg)
		require.NoError(t, err)
		assert.InDelta(t, 0.05, topo.Services["svc"].Operations["op"].ErrorRate, 0.001)
	})

	t.Run("domain resolves to generators", func(t *testing.T) {
		t.Parallel()
		resolver := func(domain string) map[string]AttributeGenerator {
			if domain == "http" {
				return map[string]AttributeGenerator{
					"http.method": &StaticValue{Value: "GET"},
					"http.route":  &StaticValue{Value: "/default"},
				}
			}
			return nil
		}
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Domain:   "http",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg, resolver)
		require.NoError(t, err)
		op := topo.Services["svc"].Operations["op"]
		require.Len(t, op.Attributes, 2)
		assert.Equal(t, "GET", op.Attributes["http.method"].Generate(nil))
		assert.Equal(t, "/default", op.Attributes["http.route"].Generate(nil))
	})

	t.Run("user attributes override domain defaults", func(t *testing.T) {
		t.Parallel()
		resolver := func(domain string) map[string]AttributeGenerator {
			return map[string]AttributeGenerator{
				"http.route": &StaticValue{Value: "/default"},
			}
		}
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Domain:   "http",
					Duration: "10ms",
					Attributes: map[string]AttributeValueConfig{
						"http.route": {Value: "/api/v1/users"},
					},
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg, resolver)
		require.NoError(t, err)
		op := topo.Services["svc"].Operations["op"]
		assert.Equal(t, "/api/v1/users", op.Attributes["http.route"].Generate(nil))
	})

	t.Run("domain with no resolver returns error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Domain:   "http",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		_, err := BuildTopology(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "domain")
		assert.Contains(t, err.Error(), "no domain resolver")
	})

	t.Run("unknown domain returns error", func(t *testing.T) {
		t.Parallel()
		resolver := func(domain string) map[string]AttributeGenerator {
			return nil
		}
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Domain:   "nonexistent",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		_, err := BuildTopology(cfg, resolver)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nonexistent")
	})

	t.Run("domain and user attributes merge", func(t *testing.T) {
		t.Parallel()
		resolver := func(domain string) map[string]AttributeGenerator {
			return map[string]AttributeGenerator{
				"http.method": &StaticValue{Value: "GET"},
			}
		}
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Domain:   "http",
					Duration: "10ms",
					Attributes: map[string]AttributeValueConfig{
						"http.route": {Value: "/api/v1/users"},
					},
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg, resolver)
		require.NoError(t, err)
		op := topo.Services["svc"].Operations["op"]
		require.Len(t, op.Attributes, 2)
		assert.Equal(t, "GET", op.Attributes["http.method"].Generate(nil))
		assert.Equal(t, "/api/v1/users", op.Attributes["http.route"].Generate(nil))
	})

	t.Run("no domain ignores resolver", func(t *testing.T) {
		t.Parallel()
		called := false
		resolver := func(domain string) map[string]AttributeGenerator {
			called = true
			return nil
		}
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		topo, err := BuildTopology(cfg, resolver)
		require.NoError(t, err)
		assert.False(t, called)
		assert.Nil(t, topo.Services["svc"].Operations["op"].Attributes)
	})
}
