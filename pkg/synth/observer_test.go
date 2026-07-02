// Tests for the SpanObserver interface and engine observer integration.
// Validates that observers receive correct span metadata during trace generation.
package synth

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type recordingObserver struct {
	mu      sync.Mutex
	records []SpanInfo
}

func (r *recordingObserver) Observe(info SpanInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, info)
}

func (r *recordingObserver) get() []SpanInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SpanInfo, len(r.records))
	copy(out, r.records)
	return out
}

func TestObserverCalledPerSpan(t *testing.T) {
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

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)
	pattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	obs := &recordingObserver{}
	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records := obs.get()
	assert.Len(t, records, 2, "observer should be called once per span")

	names := map[string]bool{}
	for _, r := range records {
		names[r.Operation] = true
	}
	assert.True(t, names["GET /users"])
	assert.True(t, names["list"])
}

func TestObserverReceivesCorrectMetadata(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name:       "svc",
			Attributes: map[string]string{"deployment.environment": "staging"},
			Operations: []OperationConfig{{
				Name:      "op",
				Duration:  "50ms",
				ErrorRate: "100%",
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

	obs := &recordingObserver{}
	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records := obs.get()
	require.Len(t, records, 1)

	info := records[0]
	assert.Equal(t, "svc", info.Service)
	assert.Equal(t, "op", info.Operation)
	assert.True(t, info.IsError)
	assert.Equal(t, trace.SpanKindServer, info.Kind)
	assert.Greater(t, info.Duration, time.Duration(0))

	attrMap := map[string]string{}
	for _, kv := range info.Attrs {
		attrMap[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal(t, "staging", attrMap["deployment.environment"])
}

func TestObserverDurationIsWallClock(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "GET /users",
					Duration: "5ms",
					Calls:    []CallConfig{{Target: "backend.query"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "query",
					Duration: "100ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)
	pattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	obs := &recordingObserver{}
	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records := obs.get()
	require.Len(t, records, 2)

	// Find the gateway (parent) span info
	var gatewayInfo SpanInfo
	for _, r := range records {
		if r.Operation == "GET /users" {
			gatewayInfo = r
			break
		}
	}

	// The gateway has 5ms own time but calls backend with 100ms duration.
	// Wall-clock duration should be >= 100ms (child time + own post-call overhead).
	assert.Greater(t, gatewayInfo.Duration, 50*time.Millisecond,
		"observer duration should reflect wall-clock time (including children), not just own processing time")
}

func TestObserverNotCalledWhenNone(t *testing.T) {
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
	engine.walkTrace(context.Background(), engine.Topology.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	assert.Len(t, spans, 1, "engine without observers should still emit spans normally")
}

func TestMultipleObservers(t *testing.T) {
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
	pattern, err := NewTrafficPattern(cfg.Traffic)
	require.NoError(t, err)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	obs1 := &recordingObserver{}
	obs2 := &recordingObserver{}
	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs1, obs2},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	assert.Len(t, obs1.get(), 1)
	assert.Len(t, obs2.get(), 1)
}

func TestObserverAttrsCopyIsolation(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name:       "svc",
			Attributes: map[string]string{"env": "test"},
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "10ms",
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

	obs := &recordingObserver{}
	engine := &Engine{
		Topology:  topo,
		Traffic:   pattern,
		Tracers:   func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records := obs.get()
	require.Len(t, records, 1)
	require.NotEmpty(t, records[0].Attrs)

	// Mutating the observer's attrs slice must not affect the engine's internal state.
	// If the slice was not copied, this mutation would corrupt shared memory.
	records[0].Attrs[0] = attribute.String("mutated", "yes")

	// Generate another span and verify attrs are not corrupted
	exporter.Reset()
	engine.walkTrace(context.Background(), topo.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records2 := obs.get()
	// Should have 2 records total (1 from first walk + 1 from second)
	require.Len(t, records2, 2)
	for _, kv := range records2[1].Attrs {
		assert.NotEqual(t, "mutated", string(kv.Key),
			"observer attrs should be an independent copy")
	}
}

// planEventRecorder captures plan events alongside spans.
type planEventRecorder struct {
	recordingObserver
	evMu   sync.Mutex
	events []PlanEvent
}

func (r *planEventRecorder) ObservePlanEvent(ev PlanEvent) {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	r.events = append(r.events, ev)
}

func (r *planEventRecorder) getEvents() []PlanEvent {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	out := make([]PlanEvent, len(r.events))
	copy(out, r.events)
	return out
}

func twoTierConfig(retries int, timeout string, errorRate string) *Config {
	return &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "request",
					Duration: "10ms",
					Calls: []CallConfig{{
						Target:  "backend.handle",
						Retries: retries,
						Timeout: timeout,
					}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:      "handle",
					Duration:  "50ms",
					ErrorRate: errorRate,
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}
}

func TestObserverReceivesParentAttribution(t *testing.T) {
	t.Parallel()

	cfg := twoTierConfig(0, "", "")
	engine, _, tp := newTestEngine(t, cfg)
	obs := &recordingObserver{}
	engine.Observers = []SpanObserver{obs}

	engine.walkTrace(context.Background(), engine.Topology.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records := obs.get()
	require.Len(t, records, 2)
	byOp := map[string]SpanInfo{}
	for _, r := range records {
		byOp[r.Operation] = r
	}

	root := byOp["request"]
	assert.Empty(t, root.ParentService, "root span has no parent")
	assert.Empty(t, root.ParentOperation)

	child := byOp["handle"]
	assert.Equal(t, "gateway", child.ParentService)
	assert.Equal(t, "request", child.ParentOperation)
}

func TestFinishSpanParentAttribution(t *testing.T) {
	t.Parallel()

	cfg := twoTierConfig(0, "", "")
	engine, _, tp := newTestEngine(t, cfg)
	obs := &recordingObserver{}

	var plans []SpanPlan
	now := time.Now()
	engine.planTrace(engine.Topology.Roots[0], nil, -1, now, 0, nil, nil, &Stats{}, &plans, new(int), DefaultMaxSpansPerTrace, false, false)
	require.Len(t, plans, 2)

	var rstats realtimeStats
	emitTrace(context.Background(), plans, now, now, func(string) trace.Tracer { return tp.Tracer("t") }, []SpanObserver{obs}, &rstats, nil)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records := obs.get()
	require.Len(t, records, 2)
	byOp := map[string]SpanInfo{}
	for _, r := range records {
		byOp[r.Operation] = r
	}
	assert.Empty(t, byOp["request"].ParentService)
	assert.Equal(t, "gateway", byOp["handle"].ParentService)
	assert.Equal(t, "request", byOp["handle"].ParentOperation)
}

func TestPlanEventObserverRetries(t *testing.T) {
	t.Parallel()

	cfg := twoTierConfig(2, "", "100%")
	engine, _, tp := newTestEngine(t, cfg)
	obs := &planEventRecorder{}
	engine.Observers = []SpanObserver{obs}

	engine.walkTrace(context.Background(), engine.Topology.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	events := obs.getEvents()
	require.Len(t, events, 2, "two retries before the final failed attempt")
	for _, ev := range events {
		assert.Equal(t, PlanEventRetry, ev.Kind)
		assert.Equal(t, "backend", ev.Service)
		assert.Equal(t, "handle", ev.Operation)
		assert.False(t, ev.Timestamp.IsZero())
	}
}

func TestPlanEventObserverTimeouts(t *testing.T) {
	t.Parallel()

	cfg := twoTierConfig(0, "1ms", "")
	engine, _, tp := newTestEngine(t, cfg)
	obs := &planEventRecorder{}
	engine.Observers = []SpanObserver{obs}

	stats := &Stats{}
	engine.walkTrace(context.Background(), engine.Topology.Roots[0], nil, time.Now(), 0, nil, nil, stats, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	events := obs.getEvents()
	require.Len(t, events, 1, "50ms child exceeds 1ms timeout")
	assert.Equal(t, PlanEventTimeout, events[0].Kind)
	assert.Equal(t, "backend", events[0].Service)
	assert.Equal(t, int64(1), stats.Timeouts)
}

func TestPlanTracePlanEvents(t *testing.T) {
	t.Parallel()

	cfg := twoTierConfig(2, "", "100%")
	engine, _, _ := newTestEngine(t, cfg)
	obs := &planEventRecorder{}
	engine.Observers = []SpanObserver{obs}

	var plans []SpanPlan
	engine.planTrace(engine.Topology.Roots[0], nil, -1, time.Now(), 0, nil, nil, &Stats{}, &plans, new(int), DefaultMaxSpansPerTrace, false, false)

	events := obs.getEvents()
	require.Len(t, events, 2)
	assert.Equal(t, PlanEventRetry, events[0].Kind)
}
