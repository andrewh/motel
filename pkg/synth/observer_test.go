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
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
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
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
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
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
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

	engine, exporter := newTestEngine(t, cfg)
	engine.walkTrace(context.Background(), engine.Topology.Roots[0], time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

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
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs1, obs2},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
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
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for testing
		Observers: []SpanObserver{obs},
	}

	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records := obs.get()
	require.Len(t, records, 1)
	require.NotEmpty(t, records[0].Attrs)

	// Mutating the observer's attrs slice must not affect the engine's internal state.
	// If the slice was not copied, this mutation would corrupt shared memory.
	records[0].Attrs[0] = attribute.String("mutated", "yes")

	// Generate another span and verify attrs are not corrupted
	exporter.Reset()
	engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	records2 := obs.get()
	// Should have 2 records total (1 from first walk + 1 from second)
	require.Len(t, records2, 2)
	for _, kv := range records2[1].Attrs {
		assert.NotEqual(t, "mutated", string(kv.Key),
			"observer attrs should be an independent copy")
	}
}
