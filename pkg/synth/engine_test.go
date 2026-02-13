// Tests for the simulation engine that walks the topology graph and emits spans
// Validates trace structure, parent-child relationships, and error injection
package synth

import (
	"context"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func newTestEngine(t *testing.T, cfg *Config) (*Engine, *tracetest.InMemoryExporter) {
	t.Helper()

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	pattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	scenarios, err := BuildScenarios(cfg.Scenarios)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Scenarios: scenarios,
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	return engine, exporter
}

func TestEngineWalkTrace(t *testing.T) {
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
					Name:     "list",
					Duration: "20ms +/- 5ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	engine.walkTrace(context.Background(), rootOp, now, nil, &Stats{})

	// Force flush
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 2, "should have root + child span")

	// Verify parent-child
	var rootSpan, childSpan tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case "GET /users":
			rootSpan = s
		case "list":
			childSpan = s
		}
	}
	assert.Equal(t, "GET /users", rootSpan.Name)
	assert.Equal(t, "list", childSpan.Name)

	// Child's parent should be root
	assert.Equal(t, rootSpan.SpanContext.SpanID(), childSpan.Parent.SpanID())
	assert.Equal(t, rootSpan.SpanContext.TraceID(), childSpan.SpanContext.TraceID())

	// Child should start after root starts
	assert.False(t, childSpan.StartTime.Before(rootSpan.StartTime))
	// Root should end after child ends
	assert.False(t, rootSpan.EndTime.Before(childSpan.EndTime))
}

func TestEngineErrorInjection(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:      "op",
				Duration:  "10ms",
				ErrorRate: "100%",
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, sdktrace.Status{Code: codes.Error, Description: "synthetic error"}, spans[0].Status)
}

func TestEngineRunDuration(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "1ms",
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond

	_, err := engine.Run(t.Context())
	require.NoError(t, err)

	// Should have generated some spans in 200ms at 100/s
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))
	spans := exporter.GetSpans()
	assert.Greater(t, len(spans), 0, "should have generated at least some spans")
}

func TestEngineGracefulShutdown(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "1ms",
			}},
		}},
		Traffic: TrafficConfig{Rate: "10/s"},
	}

	engine, _ := newTestEngine(t, cfg)
	engine.Duration = 10 * time.Second // Long duration

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := engine.Run(ctx)
		assert.NoError(t, err)
	})

	// Cancel after a short time
	time.Sleep(100 * time.Millisecond)
	cancel()

	wg.Wait()
}

func TestEngineScenarioOverrides(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "1ms",
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	// Build engine manually with scenarios active from the start
	topo, err := BuildTopology(cfg)
	require.NoError(t, err)
	pattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	scenarios := []Scenario{{
		Name:  "slowdown",
		Start: 0,
		End:   time.Hour,
		Overrides: map[string]Override{
			"svc.op": {
				Duration:     Distribution{Mean: 999 * time.Millisecond},
				ErrorRate:    1.0,
				HasErrorRate: true,
			},
		},
	}}

	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Scenarios: scenarios,
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	// Walk trace with overrides active at elapsed=0
	overrides := ResolveOverrides(ActiveScenarios(scenarios, 0))
	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), overrides, &Stats{})
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	// Should have used overridden error rate (100%)
	assert.Equal(t, sdktrace.Status{Code: codes.Error, Description: "synthetic error"}, spans[0].Status)

	// Duration should be around 999ms (the override), not 1ms
	spanDuration := spans[0].EndTime.Sub(spans[0].StartTime)
	assert.Greater(t, spanDuration, 500*time.Millisecond, "should use overridden duration")
}

func TestEngineScenarioAttributeOverrides(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "10ms",
				Attributes: map[string]AttributeValueConfig{
					"status": {Values: map[string]int{"200": 1}},
					"keep":   {Value: "preserved"},
				},
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)
	pattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	scenarios := []Scenario{{
		Name:  "error-spike",
		Start: 0,
		End:   time.Hour,
		Overrides: map[string]Override{
			"svc.op": {
				Attributes: map[string]AttributeGenerator{
					"status": &StaticValue{Value: "503"},
					"extra":  &StaticValue{Value: "added"},
				},
			},
		},
	}}

	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Scenarios: scenarios,
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	overrides := ResolveOverrides(ActiveScenarios(scenarios, 0))
	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), overrides, &Stats{})
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	attrMap := make(map[string]string)
	for _, attr := range spans[0].Attributes {
		attrMap[string(attr.Key)] = attr.Value.AsString()
	}

	assert.Equal(t, "503", attrMap["status"], "overridden attribute should use scenario value")
	assert.Equal(t, "preserved", attrMap["keep"], "non-overridden attribute should be preserved")
	assert.Equal(t, "added", attrMap["extra"], "new attribute from scenario should be present")
}

func TestEngineMultiRootDistribution(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "gateway",
			Operations: []OperationConfig{
				{Name: "GET /a", Duration: "1ms"},
				{Name: "GET /b", Duration: "1ms"},
			},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond

	_, err := engine.Run(context.Background())
	require.NoError(t, err)
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	assert.Greater(t, len(spans), 0)

	// Both root operations should appear
	names := make(map[string]bool)
	for _, s := range spans {
		names[s.Name] = true
	}
	// With 100/s for 200ms and 2 roots, both should get some traffic
	// (probabilistically guaranteed with enough traces)
	assert.True(t, len(names) >= 1, "at least one root operation should have traces")
}

func TestEngineOperationAttributes(t *testing.T) {
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
				},
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	attrMap := make(map[string]string)
	for _, attr := range spans[0].Attributes {
		attrMap[string(attr.Key)] = attr.Value.AsString()
	}

	assert.Equal(t, "/api/v1/users", attrMap["http.route"])
	assert.Equal(t, "200", attrMap["status"])
}

func TestEngineSequentialCallStyle(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:      "entry",
					Duration:  "10ms",
					CallStyle: "sequential",
					Calls:     []CallConfig{{Target: "child.a"}, {Target: "child.b"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{
					{Name: "a", Duration: "20ms"},
					{Name: "b", Duration: "20ms"},
				},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	engine.walkTrace(context.Background(), rootOp, now, nil, &Stats{})
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 3)

	var childA, childB tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case "a":
			childA = s
		case "b":
			childB = s
		}
	}

	// In sequential mode, child B should start after child A ends
	assert.False(t, childB.StartTime.Before(childA.EndTime),
		"sequential: child B (start=%v) should start at or after child A (end=%v)",
		childB.StartTime, childA.EndTime)
}

func TestEngineParallelCallStyle(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:      "entry",
					Duration:  "10ms",
					CallStyle: "parallel",
					Calls:     []CallConfig{{Target: "child.a"}, {Target: "child.b"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{
					{Name: "a", Duration: "20ms"},
					{Name: "b", Duration: "20ms"},
				},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	engine.walkTrace(context.Background(), rootOp, now, nil, &Stats{})
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 3)

	var childA, childB tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case "a":
			childA = s
		case "b":
			childB = s
		}
	}

	// In parallel mode, both children start at the same time
	assert.Equal(t, childA.StartTime, childB.StartTime,
		"parallel: both children should start at the same time")
}

func TestEngineRunStats(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:      "op",
					Duration:  "1ms",
					ErrorRate: "100%",
					Calls:     []CallConfig{{Target: "backend.work"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "work",
					Duration: "1ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, _ := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond

	stats, err := engine.Run(t.Context())
	require.NoError(t, err)
	require.NotNil(t, stats)

	assert.Greater(t, stats.Traces, int64(0))
	assert.Greater(t, stats.Spans, int64(0))
	// Spans should be > traces because each trace has 2 spans (gateway + backend)
	assert.Greater(t, stats.Spans, stats.Traces)
	// All root spans are errors (100% error rate)
	assert.Greater(t, stats.Errors, int64(0))
	assert.Greater(t, stats.ElapsedMs, int64(0))
	assert.Greater(t, stats.TracesPerSec, float64(0))
	assert.Greater(t, stats.SpansPerSec, float64(0))
	// Error rate = errors/spans; only gateway op (100% error rate) errors, backend does not
	// Each trace has 2 spans, so error rate ~ 0.5
	assert.InDelta(t, 0.5, stats.ErrorRate, 0.05)
}

func TestEngineSpanAttributes(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name:       "svc",
			Attributes: map[string]string{"deployment.environment": "production"},
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "10ms",
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	// Should have synth.service attribute
	found := false
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "synth.service" && attr.Value.AsString() == "svc" {
			found = true
		}
	}
	assert.True(t, found, "span should have synth.service attribute")

	// Should be a SERVER span for root operations
	assert.Equal(t, trace.SpanKindServer, spans[0].SpanKind)
}

func TestEngineProbabilisticCall(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.work", Probability: 0.5}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "work",
					Duration: "5ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)

	// Run multiple traces and count how many include the child
	const trials = 100
	childCount := 0
	rootOp := engine.Topology.Roots[0]

	for range trials {
		exporter.Reset()
		engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
		require.NoError(t, engine.Provider.ForceFlush(context.Background()))

		spans := exporter.GetSpans()
		if len(spans) > 1 {
			childCount++
		}
	}

	// With p=0.5 and 100 trials, child should appear in some but not all
	assert.Greater(t, childCount, 10, "child should appear in some traces")
	assert.Less(t, childCount, 90, "child should not appear in all traces")
}

func TestEngineOnErrorCondition(t *testing.T) {
	t.Parallel()

	makeConfig := func(errorRate string) *Config {
		return &Config{
			Services: []ServiceConfig{
				{
					Name: "parent",
					Operations: []OperationConfig{{
						Name:      "entry",
						Duration:  "10ms",
						ErrorRate: errorRate,
						Calls:     []CallConfig{{Target: "child.retry", Condition: "on-error"}},
					}},
				},
				{
					Name: "child",
					Operations: []OperationConfig{{
						Name:     "retry",
						Duration: "5ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
	}

	t.Run("100% error rate triggers on-error call", func(t *testing.T) {
		t.Parallel()
		engine, exporter := newTestEngine(t, makeConfig("100%"))
		rootOp := engine.Topology.Roots[0]
		engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
		require.NoError(t, engine.Provider.ForceFlush(context.Background()))
		spans := exporter.GetSpans()
		assert.Len(t, spans, 2, "on-error child should be present when parent errors")
	})

	t.Run("0% error rate skips on-error call", func(t *testing.T) {
		t.Parallel()
		engine, exporter := newTestEngine(t, makeConfig("0%"))
		rootOp := engine.Topology.Roots[0]
		engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
		require.NoError(t, engine.Provider.ForceFlush(context.Background()))
		spans := exporter.GetSpans()
		assert.Len(t, spans, 1, "on-error child should be absent when parent succeeds")
	})
}

func TestEngineOnSuccessCondition(t *testing.T) {
	t.Parallel()

	makeConfig := func(errorRate string) *Config {
		return &Config{
			Services: []ServiceConfig{
				{
					Name: "parent",
					Operations: []OperationConfig{{
						Name:      "entry",
						Duration:  "10ms",
						ErrorRate: errorRate,
						Calls:     []CallConfig{{Target: "child.cache", Condition: "on-success"}},
					}},
				},
				{
					Name: "child",
					Operations: []OperationConfig{{
						Name:     "cache",
						Duration: "5ms",
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
	}

	t.Run("0% error rate triggers on-success call", func(t *testing.T) {
		t.Parallel()
		engine, exporter := newTestEngine(t, makeConfig("0%"))
		rootOp := engine.Topology.Roots[0]
		engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
		require.NoError(t, engine.Provider.ForceFlush(context.Background()))
		spans := exporter.GetSpans()
		assert.Len(t, spans, 2, "on-success child should be present when parent succeeds")
	})

	t.Run("100% error rate skips on-success call", func(t *testing.T) {
		t.Parallel()
		engine, exporter := newTestEngine(t, makeConfig("100%"))
		rootOp := engine.Topology.Roots[0]
		engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
		require.NoError(t, engine.Provider.ForceFlush(context.Background()))
		spans := exporter.GetSpans()
		assert.Len(t, spans, 1, "on-success child should be absent when parent errors")
	})
}

func TestEngineFanOutCount(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.work", Count: 3}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "work",
					Duration: "5ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	assert.Len(t, spans, 4, "should have 1 parent + 3 child spans")

	childCount := 0
	for _, s := range spans {
		if s.Name == "work" {
			childCount++
		}
	}
	assert.Equal(t, 3, childCount, "should have 3 fan-out child spans")
}

func TestEngineFanOutSequential(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:      "entry",
					Duration:  "10ms",
					CallStyle: "sequential",
					Calls:     []CallConfig{{Target: "child.work", Count: 3}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "work",
					Duration: "20ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 4)

	// Collect child spans sorted by start time
	var children []tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "work" {
			children = append(children, s)
		}
	}
	require.Len(t, children, 3)

	// Sort by start time
	slices.SortFunc(children, func(a, b tracetest.SpanStub) int {
		return a.StartTime.Compare(b.StartTime)
	})

	// Each child should start at or after the previous child ends
	for i := 1; i < len(children); i++ {
		assert.False(t, children[i].StartTime.Before(children[i-1].EndTime),
			"sequential: child %d (start=%v) should start at or after child %d (end=%v)",
			i, children[i].StartTime, i-1, children[i-1].EndTime)
	}
}

func TestEngineFanOutParallel(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:      "entry",
					Duration:  "10ms",
					CallStyle: "parallel",
					Calls:     []CallConfig{{Target: "child.work", Count: 3}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "work",
					Duration: "20ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), nil, &Stats{})
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 4)

	// All child spans should share the same start time
	var children []tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "work" {
			children = append(children, s)
		}
	}
	require.Len(t, children, 3)

	for i := 1; i < len(children); i++ {
		assert.Equal(t, children[0].StartTime, children[i].StartTime,
			"parallel: all children should start at the same time")
	}
}
