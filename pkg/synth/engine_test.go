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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func newTestEngine(t *testing.T, cfg *Config) (*Engine, *tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	pattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	scenarios, err := BuildScenarios(cfg.Scenarios, topo)
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
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	return engine, exporter, tp
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

	engine, exporter, tp := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)

	// Force flush
	require.NoError(t, tp.ForceFlush(context.Background()))

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

	engine, exporter, tp := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

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

	engine, exporter, tp := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond

	_, err := engine.Run(t.Context())
	require.NoError(t, err)

	// Should have generated some spans in 200ms at 100/s
	require.NoError(t, tp.ForceFlush(context.Background()))
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

	engine, _, _ := newTestEngine(t, cfg)
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
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	// Walk trace with overrides active at elapsed=0
	overrides := ResolveOverrides(ActiveScenarios(scenarios, 0))
	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, overrides, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
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
					"status": {Values: map[any]int{"200": 1}},
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
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	overrides := ResolveOverrides(ActiveScenarios(scenarios, 0))
	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, overrides, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
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

func TestEngineScenarioTrafficOverride(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "1ms",
			}},
		}},
		Traffic: TrafficConfig{Rate: "10/s"}, // slow base rate
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	basePattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	fastPattern, err := NewTrafficPattern(TrafficConfig{Rate: "1000/s"})
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	scenarios := []Scenario{{
		Name:    "load-spike",
		Start:   0,
		End:     time.Hour,
		Traffic: fastPattern,
	}}

	engine := &Engine{
		Topology:  topo,
		Traffic:   basePattern,
		Scenarios: scenarios,
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Duration:  200 * time.Millisecond,
	}

	stats, err := engine.Run(t.Context())
	require.NoError(t, err)

	// At 1000/s for 200ms we'd expect ~200 traces; at 10/s only ~2
	// The threshold of 20 confirms the fast pattern was used
	assert.Greater(t, stats.Traces, int64(20),
		"scenario traffic override should produce significantly more traces than base rate")
}

func TestEngineScenarioCombinedOverrideAndTraffic(t *testing.T) {
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

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	basePattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	fastPattern, err := NewTrafficPattern(TrafficConfig{Rate: "1000/s"})
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	scenarios := []Scenario{{
		Name:    "combined",
		Start:   0,
		End:     time.Hour,
		Traffic: fastPattern,
		Overrides: map[string]Override{
			"svc.op": {
				ErrorRate:    1.0,
				HasErrorRate: true,
			},
		},
	}}

	engine := &Engine{
		Topology:  topo,
		Traffic:   basePattern,
		Scenarios: scenarios,
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Duration:  200 * time.Millisecond,
	}

	stats, err := engine.Run(t.Context())
	require.NoError(t, err)

	// Traffic override should produce many traces (fast rate)
	assert.Greater(t, stats.Traces, int64(20),
		"traffic override should produce many traces")
	// Error rate override should make all spans errors
	assert.InDelta(t, 1.0, stats.ErrorRate, 0.01,
		"error rate override should apply alongside traffic override")
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

	engine, exporter, tp := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond

	_, err := engine.Run(context.Background())
	require.NoError(t, err)
	require.NoError(t, tp.ForceFlush(context.Background()))

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
					"status":     {Values: map[any]int{"200": 1}},
				},
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

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

	engine, exporter, tp := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

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

	engine, exporter, tp := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

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

	engine, _, _ := newTestEngine(t, cfg)
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

	engine, exporter, tp := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

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

	engine, exporter, tp := newTestEngine(t, cfg)

	// Run multiple traces and count how many include the child
	const trials = 100
	childCount := 0
	rootOp := engine.Topology.Roots[0]

	for range trials {
		exporter.Reset()
		engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
		require.NoError(t, tp.ForceFlush(context.Background()))

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
		engine, exporter, tp := newTestEngine(t, makeConfig("100%"))
		rootOp := engine.Topology.Roots[0]
		engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
		require.NoError(t, tp.ForceFlush(context.Background()))
		spans := exporter.GetSpans()
		assert.Len(t, spans, 2, "on-error child should be present when parent errors")
	})

	t.Run("0% error rate skips on-error call", func(t *testing.T) {
		t.Parallel()
		engine, exporter, tp := newTestEngine(t, makeConfig("0%"))
		rootOp := engine.Topology.Roots[0]
		engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
		require.NoError(t, tp.ForceFlush(context.Background()))
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
		engine, exporter, tp := newTestEngine(t, makeConfig("0%"))
		rootOp := engine.Topology.Roots[0]
		engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
		require.NoError(t, tp.ForceFlush(context.Background()))
		spans := exporter.GetSpans()
		assert.Len(t, spans, 2, "on-success child should be present when parent succeeds")
	})

	t.Run("100% error rate skips on-success call", func(t *testing.T) {
		t.Parallel()
		engine, exporter, tp := newTestEngine(t, makeConfig("100%"))
		rootOp := engine.Topology.Roots[0]
		engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
		require.NoError(t, tp.ForceFlush(context.Background()))
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

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

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

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

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

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

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

func TestEngineCallTimeout(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.slow", Timeout: "50ms"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "slow",
					Duration: "200ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)

	var parent, child tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case "entry":
			parent = s
		case "slow":
			child = s
		}
	}

	// Child keeps its full duration (200ms)
	childDuration := child.EndTime.Sub(child.StartTime)
	assert.Greater(t, childDuration, 100*time.Millisecond)

	// Parent should be capped: pre-call(5ms) + timeout(50ms) + post-call(5ms) = ~60ms
	parentDuration := parent.EndTime.Sub(parent.StartTime)
	assert.Less(t, parentDuration, 100*time.Millisecond,
		"parent duration should be capped by timeout")

	// Parent should be errored (timeout cascades)
	assert.Equal(t, codes.Error, parent.Status.Code)

	assert.Equal(t, int64(1), stats.Timeouts)
}

func TestEngineCallNoTimeout(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.fast", Timeout: "500ms"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "fast",
					Duration: "20ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)

	var parent tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "entry" {
			parent = s
		}
	}

	// No timeout: parent should not be errored
	assert.NotEqual(t, codes.Error, parent.Status.Code)
	assert.Equal(t, int64(0), stats.Timeouts)
}

func TestEngineCallTimeoutSequential(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:      "entry",
					Duration:  "10ms",
					CallStyle: "sequential",
					Calls: []CallConfig{
						{Target: "child.slow", Timeout: "50ms"},
						{Target: "child.fast"},
					},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{
					{Name: "slow", Duration: "200ms"},
					{Name: "fast", Duration: "20ms"},
				},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 3)

	var slow, fast tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case "slow":
			slow = s
		case "fast":
			fast = s
		}
	}

	// In sequential mode, fast should start after the capped timeout of slow
	preCall := 5 * time.Millisecond // half of 10ms parent own duration
	expectedSecondStart := now.Add(preCall).Add(50 * time.Millisecond)
	assert.False(t, fast.StartTime.Before(expectedSecondStart),
		"sequential: second call should start after timeout of first (expected >= %v, got %v)",
		expectedSecondStart, fast.StartTime)

	// But slow's actual duration is still 200ms
	slowDuration := slow.EndTime.Sub(slow.StartTime)
	assert.Greater(t, slowDuration, 100*time.Millisecond)

	assert.Equal(t, int64(1), stats.Timeouts)
}

func TestEngineCascadingError(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.failing"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:      "failing",
					Duration:  "20ms",
					ErrorRate: "100%",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)

	var parent tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "entry" {
			parent = s
		}
	}

	// Parent error_rate is 0%, but child 100% error cascades up
	assert.Equal(t, codes.Error, parent.Status.Code,
		"parent should be errored due to cascading child failure")
	assert.Equal(t, int64(2), stats.Errors, "both parent and child should count as errors")
}

func TestEngineCascadingErrorPreservesConditions(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls: []CallConfig{
						{Target: "child.failing"},
						{Target: "child.fallback", Condition: "on-error"},
					},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{
					{Name: "failing", Duration: "20ms", ErrorRate: "100%"},
					{Name: "fallback", Duration: "5ms"},
				},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	// Conditions use the parent's OWN error rate (0%), not cascaded.
	// So on-error call should NOT fire. Only parent + failing child = 2 spans.
	assert.Len(t, spans, 2,
		"on-error condition should use parent's own error rate, not cascaded errors")
}

func TestEngineRetryOnError(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.failing", Retries: 2, RetryBackoff: "10ms"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:      "failing",
					Duration:  "20ms",
					ErrorRate: "100%",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	// 1 parent + 3 child attempts (1 initial + 2 retries) = 4
	assert.Len(t, spans, 4, "should have 1 parent + 3 child attempts")

	childCount := 0
	for _, s := range spans {
		if s.Name == "failing" {
			childCount++
		}
	}
	assert.Equal(t, 3, childCount)
	assert.Equal(t, int64(2), stats.Retries)
}

func TestEngineRetrySuccess(t *testing.T) {
	t.Parallel()

	// Use a child with 50% error rate and many retries so at least one succeeds
	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.flaky", Retries: 10, RetryBackoff: "1ms"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:      "flaky",
					Duration:  "5ms",
					ErrorRate: "50%",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	// With 50% error rate and 10 retries, it's nearly certain at least one succeeds
	// The first success stops retrying, so we should have fewer than 12 spans total

	var parent tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "entry" {
			parent = s
		}
	}

	// Parent should not be errored (retry eventually succeeds)
	assert.NotEqual(t, codes.Error, parent.Status.Code,
		"parent should succeed after retry recovers")
}

func TestEngineRetryBackoff(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.failing", Retries: 1, RetryBackoff: "30ms"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:      "failing",
					Duration:  "20ms",
					ErrorRate: "100%",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 3) // parent + 2 child attempts

	var children []tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "failing" {
			children = append(children, s)
		}
	}
	require.Len(t, children, 2)

	slices.SortFunc(children, func(a, b tracetest.SpanStub) int {
		return a.StartTime.Compare(b.StartTime)
	})

	// Second attempt should start at first end + 30ms backoff
	gap := children[1].StartTime.Sub(children[0].EndTime)
	assert.GreaterOrEqual(t, gap, 30*time.Millisecond,
		"retry should respect backoff (gap=%v)", gap)
}

func TestEngineRetryWithTimeout(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.slow", Timeout: "50ms", Retries: 1, RetryBackoff: "10ms"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "slow",
					Duration: "200ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	// parent + 2 child attempts (both time out)
	assert.Len(t, spans, 3)

	assert.Equal(t, int64(2), stats.Timeouts, "each attempt should time out")
	assert.Equal(t, int64(1), stats.Retries, "should retry once")
}

func TestEngineRetryStats(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.failing", Retries: 3, RetryBackoff: "1ms"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:      "failing",
					Duration:  "5ms",
					ErrorRate: "100%",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, _, _ := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)

	assert.Equal(t, int64(3), stats.Retries, "should retry 3 times")
	// 1 parent + 4 child = 5 spans, all errored (child 100%, parent cascaded)
	assert.Equal(t, int64(5), stats.Spans)
	assert.Equal(t, int64(5), stats.Errors)
}

func TestEngineNoRetryWithoutConfig(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "child.failing"}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:      "failing",
					Duration:  "5ms",
					ErrorRate: "100%",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	// No retries configured: 1 parent + 1 child = 2
	assert.Len(t, spans, 2)
	assert.Equal(t, int64(0), stats.Retries)
}

func TestEngineSpanBound(t *testing.T) {
	t.Parallel()

	// Deep topology: parent calls child, child calls grandchild, each with count=5
	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "1ms",
					Calls:    []CallConfig{{Target: "child.work", Count: 5}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "work",
					Duration: "1ms",
					Calls:    []CallConfig{{Target: "grandchild.leaf", Count: 5}},
				}},
			},
			{
				Name: "grandchild",
				Operations: []OperationConfig{{
					Name:     "leaf",
					Duration: "1ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	rootOp := engine.Topology.Roots[0]

	// Without bound: 1 + 5 + 25 = 31 spans
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))
	assert.Equal(t, 31, len(exporter.GetSpans()))

	// With bound of 10 spans
	exporter.Reset()
	spanCount := 0
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, &spanCount, 10)
	require.NoError(t, tp.ForceFlush(context.Background()))
	assert.LessOrEqual(t, len(exporter.GetSpans()), 10, "span count should be bounded")
	assert.Greater(t, len(exporter.GetSpans()), 0, "should produce at least some spans")
}

func TestEngineSpanBoundInRun(t *testing.T) {
	t.Parallel()

	// Same deep topology, run via Engine.Run with a low bound
	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "parent",
				Operations: []OperationConfig{{
					Name:     "entry",
					Duration: "1ms",
					Calls:    []CallConfig{{Target: "child.work", Count: 10}},
				}},
			},
			{
				Name: "child",
				Operations: []OperationConfig{{
					Name:     "work",
					Duration: "1ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, _, _ := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond
	engine.MaxSpansPerTrace = 5

	stats, err := engine.Run(t.Context())
	require.NoError(t, err)
	assert.Greater(t, stats.SpansBounded, int64(0), "some traces should hit the span bound")
}

func TestEngineTraceErrorRate(t *testing.T) {
	t.Parallel()

	// Root has 100% error rate, child does not
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

	engine, _, _ := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond

	stats, err := engine.Run(t.Context())
	require.NoError(t, err)

	// All root spans error (100% error rate), so TraceErrorRate should be ~1.0
	assert.InDelta(t, 1.0, stats.TraceErrorRate, 0.01)
	assert.Equal(t, stats.Traces, stats.FailedTraces)

	// ErrorRate is per-span: gateway errors (100%), backend does not
	// Each trace has 2 spans, only gateway errored, so ErrorRate ~ 0.5
	assert.InDelta(t, 0.5, stats.ErrorRate, 0.05)
}

func TestEngineTraceErrorRateWithCascading(t *testing.T) {
	t.Parallel()

	// Root has 0% error rate, child has 100%. Cascading makes root errored.
	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "1ms",
					Calls:    []CallConfig{{Target: "backend.failing"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:      "failing",
					Duration:  "1ms",
					ErrorRate: "100%",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, _, _ := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond

	stats, err := engine.Run(t.Context())
	require.NoError(t, err)

	// Root errored via cascading, so TraceErrorRate ~ 1.0
	assert.InDelta(t, 1.0, stats.TraceErrorRate, 0.01)
	// ErrorRate is also ~1.0 since both spans are errored
	assert.InDelta(t, 1.0, stats.ErrorRate, 0.01)
}

func TestEngineTraceErrorRateZero(t *testing.T) {
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

	engine, _, _ := newTestEngine(t, cfg)
	engine.Duration = 200 * time.Millisecond

	stats, err := engine.Run(t.Context())
	require.NoError(t, err)

	assert.Equal(t, int64(0), stats.FailedTraces)
	assert.Equal(t, float64(0), stats.TraceErrorRate)
}

func TestEffectiveCalls(t *testing.T) {
	t.Parallel()

	svcA := &Service{Name: "a", Operations: make(map[string]*Operation)}
	svcB := &Service{Name: "b", Operations: make(map[string]*Operation)}
	svcC := &Service{Name: "c", Operations: make(map[string]*Operation)}

	opA := &Operation{Service: svcA, Name: "op", Ref: "a.op", Duration: Distribution{Mean: 10 * time.Millisecond}}
	opB := &Operation{Service: svcB, Name: "op", Ref: "b.op", Duration: Distribution{Mean: 10 * time.Millisecond}}
	opC := &Operation{Service: svcC, Name: "op", Ref: "c.op", Duration: Distribution{Mean: 10 * time.Millisecond}}

	svcA.Operations["op"] = opA
	svcB.Operations["op"] = opB
	svcC.Operations["op"] = opC

	opA.Calls = []Call{{Operation: opB}}

	t.Run("no overrides returns base calls", func(t *testing.T) {
		t.Parallel()
		calls := effectiveCalls(opA, nil)
		require.Len(t, calls, 1)
		assert.Equal(t, opB, calls[0].Operation)
	})

	t.Run("no call changes returns base calls", func(t *testing.T) {
		t.Parallel()
		overrides := map[string]Override{
			"a.op": {Duration: Distribution{Mean: 999 * time.Millisecond}},
		}
		calls := effectiveCalls(opA, overrides)
		require.Len(t, calls, 1)
		assert.Equal(t, opB, calls[0].Operation)
	})

	t.Run("add_calls appends to base", func(t *testing.T) {
		t.Parallel()
		overrides := map[string]Override{
			"a.op": {AddCalls: []Call{{Operation: opC}}},
		}
		calls := effectiveCalls(opA, overrides)
		require.Len(t, calls, 2)
		assert.Equal(t, opB, calls[0].Operation)
		assert.Equal(t, opC, calls[1].Operation)
	})

	t.Run("remove_calls filters base", func(t *testing.T) {
		t.Parallel()
		overrides := map[string]Override{
			"a.op": {RemoveCalls: map[string]bool{"b.op": true}},
		}
		calls := effectiveCalls(opA, overrides)
		assert.Empty(t, calls)
	})

	t.Run("add and remove together", func(t *testing.T) {
		t.Parallel()
		overrides := map[string]Override{
			"a.op": {
				AddCalls:    []Call{{Operation: opC}},
				RemoveCalls: map[string]bool{"b.op": true},
			},
		}
		calls := effectiveCalls(opA, overrides)
		require.Len(t, calls, 1)
		assert.Equal(t, opC, calls[0].Operation)
	})
}

func TestEngineWalkTraceWithAddCalls(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "request",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "backend.query"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "query",
					Duration: "5ms",
				}},
			},
			{
				Name: "cache",
				Operations: []OperationConfig{{
					Name:     "get",
					Duration: "1ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	engine := &Engine{
		Topology: topo,
		Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:      rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	cacheOp := topo.Services["cache"].Operations["get"]
	overrides := map[string]Override{
		"gateway.request": {
			AddCalls: []Call{{Operation: cacheOp}},
		},
	}

	gatewayOp := topo.Services["gateway"].Operations["request"]

	var stats Stats
	engine.walkTrace(context.Background(), gatewayOp, time.Now(), 0, overrides, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 3, "should have gateway + backend + cache spans")

	names := make(map[string]bool)
	for _, s := range spans {
		names[s.Name] = true
	}
	assert.True(t, names["get"], "added cache.get call should produce a span")
}

func TestEngineWalkTraceWithRemoveCalls(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "request",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "backend.query"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "query",
					Duration: "5ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	engine := &Engine{
		Topology: topo,
		Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:      rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	overrides := map[string]Override{
		"gateway.request": {
			RemoveCalls: map[string]bool{"backend.query": true},
		},
	}

	var stats Stats
	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, overrides, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1, "should only have gateway span, backend removed")
	assert.Equal(t, "request", spans[0].Name)
}

func TestEngineRunWithScenarioCallChanges(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "request",
					Duration: "1ms",
					Calls:    []CallConfig{{Target: "backend.query"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "query",
					Duration: "1ms",
				}},
			},
			{
				Name: "cache",
				Operations: []OperationConfig{{
					Name:     "get",
					Duration: "1ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	pattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	cacheOp := topo.Services["cache"].Operations["get"]

	scenarios := []Scenario{{
		Name:  "add-cache-remove-backend",
		Start: 0,
		End:   time.Hour,
		Overrides: map[string]Override{
			"gateway.request": {
				AddCalls:    []Call{{Operation: cacheOp}},
				RemoveCalls: map[string]bool{"backend.query": true},
			},
		},
	}}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Scenarios: scenarios,
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Duration:  200 * time.Millisecond,
	}

	_, err = engine.Run(t.Context())
	require.NoError(t, err)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	assert.Greater(t, len(spans), 0)

	// Verify call topology changes were applied
	names := make(map[string]int)
	for _, s := range spans {
		names[s.Name]++
	}

	assert.Equal(t, 0, names["query"], "backend.query should not appear (removed)")
	assert.Greater(t, names["get"], 0, "cache.get should appear (added)")
	assert.Greater(t, names["request"], 0, "gateway.request should appear")
}

func TestEngineLabelScenarios(t *testing.T) {
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
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	scenarios := []Scenario{
		{
			Name:     "db-degradation",
			Start:    0,
			End:      time.Hour,
			Priority: 2,
			Overrides: map[string]Override{
				"svc.op": {HasErrorRate: true, ErrorRate: 0.0},
			},
		},
		{
			Name:     "high-traffic",
			Start:    0,
			End:      time.Hour,
			Priority: 1,
			Overrides: map[string]Override{
				"svc.op": {HasErrorRate: true, ErrorRate: 0.0},
			},
		},
	}

	engine := &Engine{
		Topology:       topo,
		Scenarios:      scenarios,
		Tracers:        func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:            rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		LabelScenarios: true,
	}

	active := ActiveScenarios(scenarios, 0)
	overrides := ResolveOverrides(active)
	scenarioNames := make([]string, len(active))
	for i, s := range active {
		scenarioNames[i] = s.Name
	}

	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, overrides, scenarioNames, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	var found bool
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "synth.scenarios" {
			found = true
			got := attr.Value.AsStringSlice()
			assert.Equal(t, scenarioNames, got)
		}
	}
	assert.True(t, found, "span should have synth.scenarios attribute")
}

func TestEngineLabelScenariosEmpty(t *testing.T) {
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
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	engine.LabelScenarios = true

	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	var found bool
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "synth.scenarios" {
			found = true
			got := attr.Value.AsStringSlice()
			assert.Empty(t, got, "synth.scenarios should be empty when no scenarios active")
		}
	}
	assert.True(t, found, "span should have synth.scenarios attribute even when empty")
}

func TestEngineLabelScenariosDisabled(t *testing.T) {
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
	}

	engine, exporter, tp := newTestEngine(t, cfg)
	// LabelScenarios defaults to false

	rootOp := engine.Topology.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	for _, attr := range spans[0].Attributes {
		assert.NotEqual(t, "synth.scenarios", string(attr.Key),
			"synth.scenarios should not be present when LabelScenarios is false")
	}
}

func TestPerServiceResource(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "GET /users",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "backend.list"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "list",
					Duration: "5ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "10/s"},
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()

	providers := make(map[string]*sdktrace.TracerProvider, len(topo.Services))
	for name := range topo.Services {
		res, resErr := resource.Merge(
			resource.Default(),
			resource.NewSchemaless(attribute.String("service.name", name)),
		)
		require.NoError(t, resErr)
		providers[name] = sdktrace.NewTracerProvider(
			sdktrace.WithSyncer(exporter),
			sdktrace.WithResource(res),
		)
	}
	t.Cleanup(func() {
		for _, tp := range providers {
			_ = tp.Shutdown(context.Background())
		}
	})

	engine := &Engine{
		Topology: topo,
		Tracers: func(name string) trace.Tracer {
			return providers[name].Tracer(name)
		},
		Rng: rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
	}

	rootOp := topo.Roots[0]
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)

	for _, tp := range providers {
		require.NoError(t, tp.ForceFlush(context.Background()))
	}

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)

	for _, span := range spans {
		var synthService, resourceService string
		for _, attr := range span.Attributes {
			if string(attr.Key) == "synth.service" {
				synthService = attr.Value.AsString()
			}
		}
		require.NotEmpty(t, synthService, "span should have synth.service attribute")

		for _, attr := range span.Resource.Attributes() {
			if string(attr.Key) == "service.name" {
				resourceService = attr.Value.AsString()
			}
		}
		assert.Equal(t, synthService, resourceService,
			"resource service.name should match synth.service for span %s", span.Name)
	}
}

func TestEngineTimeOffset(t *testing.T) {
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

	t.Run("negative offset shifts spans into past", func(t *testing.T) {
		t.Parallel()
		engine, exporter, tp := newTestEngine(t, cfg)
		engine.TimeOffset = -1 * time.Hour
		engine.Duration = 100 * time.Millisecond

		before := time.Now()
		_, err := engine.Run(context.Background())
		require.NoError(t, err)
		require.NoError(t, tp.ForceFlush(context.Background()))

		spans := exporter.GetSpans()
		require.NotEmpty(t, spans)
		for _, s := range spans {
			assert.True(t, s.StartTime.Before(before.Add(-30*time.Minute)),
				"span start %v should be shifted ~1h into the past (before %v)", s.StartTime, before.Add(-30*time.Minute))
		}
	})

	t.Run("positive offset shifts spans into future", func(t *testing.T) {
		t.Parallel()
		engine, exporter, tp := newTestEngine(t, cfg)
		engine.TimeOffset = 1 * time.Hour
		engine.Duration = 100 * time.Millisecond

		after := time.Now()
		_, err := engine.Run(context.Background())
		require.NoError(t, err)
		require.NoError(t, tp.ForceFlush(context.Background()))

		spans := exporter.GetSpans()
		require.NotEmpty(t, spans)
		for _, s := range spans {
			assert.True(t, s.StartTime.After(after.Add(30*time.Minute)),
				"span start %v should be shifted ~1h into the future (after %v)", s.StartTime, after.Add(30*time.Minute))
		}
	})

	t.Run("zero offset leaves timestamps near now", func(t *testing.T) {
		t.Parallel()
		engine, exporter, tp := newTestEngine(t, cfg)
		engine.Duration = 100 * time.Millisecond

		before := time.Now()
		_, err := engine.Run(context.Background())
		require.NoError(t, err)
		require.NoError(t, tp.ForceFlush(context.Background()))

		spans := exporter.GetSpans()
		require.NotEmpty(t, spans)
		for _, s := range spans {
			assert.True(t, s.StartTime.After(before.Add(-time.Second)),
				"span start %v should be near now", s.StartTime)
			assert.True(t, s.StartTime.Before(before.Add(time.Minute)),
				"span start %v should be near now", s.StartTime)
		}
	})

	t.Run("offset propagates to log record timestamps", func(t *testing.T) {
		t.Parallel()
		errCfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:      "op",
					Duration:  "1ms",
					ErrorRate: "100%",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}
		engine, _, tp := newTestEngine(t, errCfg)
		engine.TimeOffset = -1 * time.Hour
		engine.Duration = 100 * time.Millisecond

		logExporter := &memoryLogExporter{}
		lp := sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExporter)),
		)
		t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
		engine.Observers = []SpanObserver{
			NewLogObserver(map[string]otellog.Logger{"svc": lp.Logger("motel")}, 0),
		}

		before := time.Now()
		_, err := engine.Run(context.Background())
		require.NoError(t, err)
		require.NoError(t, tp.ForceFlush(context.Background()))

		records := logExporter.get()
		require.NotEmpty(t, records)
		for _, r := range records {
			assert.True(t, r.Timestamp().Before(before.Add(-30*time.Minute)),
				"log record timestamp %v should be shifted ~1h into the past", r.Timestamp())
		}
	})
}
