// Tests for OTel baggage declaration, propagation, and attribute surfacing.
package synth

import (
	"context"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// baggageAttrs extracts the baggage.<key> attributes from a captured span,
// returning them as a plain map keyed by the bare baggage key.
func baggageAttrs(span tracetest.SpanStub) map[string]string {
	out := map[string]string{}
	for _, a := range span.Attributes {
		if k, ok := strings.CutPrefix(string(a.Key), baggageAttributePrefix); ok {
			out[k] = a.Value.AsString()
		}
	}
	return out
}

func findStub(spans []tracetest.SpanStub, name string) (tracetest.SpanStub, bool) {
	for _, s := range spans {
		if s.Name == name {
			return s, true
		}
	}
	return tracetest.SpanStub{}, false
}

// baggageDemoConfig models a gateway that declares baggage, a payments service
// that surfaces it as attributes and overrides one key, and a ledger service
// that inherits the override.
func baggageDemoConfig() *Config {
	surface := true
	return &Config{
		Services: []ServiceConfig{
			{
				Name:    "gateway",
				Baggage: map[string]string{"tenant.id": "acme", "session.plan": "enterprise"},
				Operations: []OperationConfig{{
					Name:     "checkout",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "payments.charge"}},
				}},
			},
			{
				Name:                "payments",
				BaggageAsAttributes: &surface,
				Operations: []OperationConfig{{
					Name:     "charge",
					Duration: "10ms",
					Baggage:  map[string]string{"tenant.id": "acme-payments"},
					Calls:    []CallConfig{{Target: "ledger.record"}},
				}},
			},
			{
				Name:                "ledger",
				BaggageAsAttributes: &surface,
				Operations: []OperationConfig{{
					Name:     "record",
					Duration: "5ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}
}

func TestValidateBaggage(t *testing.T) {
	t.Parallel()

	base := func(bag map[string]string) *Config {
		return &Config{
			Services: []ServiceConfig{{
				Name:    "svc",
				Baggage: bag,
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}
	}

	t.Run("valid dotted key", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, ValidateConfig(base(map[string]string{"user.id": "u-1"})))
	})

	t.Run("empty key rejected", func(t *testing.T) {
		t.Parallel()
		err := ValidateConfig(base(map[string]string{"": "v"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "baggage key must not be empty")
	})

	t.Run("invalid token key rejected", func(t *testing.T) {
		t.Parallel()
		err := ValidateConfig(base(map[string]string{"bad key": "v"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid baggage key")
	})

	t.Run("operation-level baggage validated", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
					Baggage:  map[string]string{"bad key": "v"},
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}
		err := ValidateConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid baggage key")
	})
}

func TestBuildTopologyBaggageResolution(t *testing.T) {
	t.Parallel()

	topo, err := BuildTopology(baggageDemoConfig())
	require.NoError(t, err)

	charge := topo.Services["payments"].Operations["charge"]
	// Service-level baggage is empty here, operation declares tenant.id only.
	assert.Equal(t, map[string]string{"tenant.id": "acme-payments"}, charge.Baggage)
	assert.True(t, charge.BaggageAsAttributes, "operation inherits service baggage_as_attributes default")

	checkout := topo.Services["gateway"].Operations["checkout"]
	assert.Equal(t, map[string]string{"tenant.id": "acme", "session.plan": "enterprise"}, checkout.Baggage)
	assert.False(t, checkout.BaggageAsAttributes)
}

func TestBuildTopologyBaggageOperationOverridesServiceFlag(t *testing.T) {
	t.Parallel()

	svcOn, opOff := true, false
	cfg := &Config{
		Services: []ServiceConfig{{
			Name:                "svc",
			Baggage:             map[string]string{"a": "1"},
			BaggageAsAttributes: &svcOn,
			Operations: []OperationConfig{
				{Name: "keep", Duration: "5ms"},
				{Name: "opt-out", Duration: "5ms", BaggageAsAttributes: &opOff, Baggage: map[string]string{"b": "2"}},
			},
		}},
		Traffic: TrafficConfig{Rate: "10/s"},
	}
	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	assert.True(t, topo.Services["svc"].Operations["keep"].BaggageAsAttributes)
	assert.False(t, topo.Services["svc"].Operations["opt-out"].BaggageAsAttributes)
	// Operation baggage overlays service baggage (operation wins on conflicts).
	assert.Equal(t, map[string]string{"a": "1", "b": "2"}, topo.Services["svc"].Operations["opt-out"].Baggage)
}

// TestEngineBaggagePropagation drives the batch walkTrace path and asserts that
// baggage declared upstream is inherited and surfaced as attributes downstream,
// including an operation-level override that propagates to descendants.
func TestEngineBaggagePropagation(t *testing.T) {
	t.Parallel()

	engine, exporter, tp := newTestEngine(t, baggageDemoConfig())

	root := engine.Topology.Roots[0]
	require.Equal(t, "checkout", root.Name)

	stats := &Stats{}
	engine.walkTrace(context.Background(), root, nil, time.Now(), 0, nil, nil, stats, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()

	// gateway.checkout does not surface baggage (no baggage_as_attributes).
	checkout, ok := findStub(spans, "checkout")
	require.True(t, ok)
	assert.Empty(t, baggageAttrs(checkout), "gateway span should not surface baggage attributes")

	// payments.charge surfaces inherited session.plan plus its overridden tenant.id.
	charge, ok := findStub(spans, "charge")
	require.True(t, ok)
	assert.Equal(t, map[string]string{
		"tenant.id":    "acme-payments",
		"session.plan": "enterprise",
	}, baggageAttrs(charge))

	// ledger.record inherits the override from charge, proving propagation to descendants.
	record, ok := findStub(spans, "record")
	require.True(t, ok)
	assert.Equal(t, map[string]string{
		"tenant.id":    "acme-payments",
		"session.plan": "enterprise",
	}, baggageAttrs(record))
}

// baggageCapture is a SpanProcessor that records the OTel baggage present on the
// context at span start — the vantage point a real collector/processor has.
type baggageCapture struct {
	mu     sync.Mutex
	byName map[string]map[string]string
}

func (b *baggageCapture) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	m := map[string]string{}
	for _, mem := range baggage.FromContext(parent).Members() {
		m[mem.Key()] = mem.Value()
	}
	b.mu.Lock()
	b.byName[s.Name()] = m
	b.mu.Unlock()
}

func (b *baggageCapture) OnEnd(sdktrace.ReadOnlySpan)      {}
func (b *baggageCapture) Shutdown(context.Context) error   { return nil }
func (b *baggageCapture) ForceFlush(context.Context) error { return nil }

// TestEngineBaggageOnContext verifies baggage propagates as real OTel baggage on
// the context (not just as attributes), observable by a downstream processor.
func TestEngineBaggageOnContext(t *testing.T) {
	t.Parallel()

	capture := &baggageCapture{byName: map[string]map[string]string{}}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(capture))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	topo, err := BuildTopology(baggageDemoConfig())
	require.NoError(t, err)
	engine := &Engine{
		Topology: topo,
		Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:      rand.New(rand.NewPCG(1, 2)), //nolint:gosec // deterministic test seed
	}

	engine.walkTrace(context.Background(), topo.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace, false, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	// The gateway span starts with its declared baggage on the context.
	assert.Equal(t, map[string]string{"tenant.id": "acme", "session.plan": "enterprise"}, capture.byName["checkout"])
	// The ledger span sees the overridden tenant.id propagated down the context.
	assert.Equal(t, map[string]string{"tenant.id": "acme-payments", "session.plan": "enterprise"}, capture.byName["record"])
}

// TestPlanBaggagePropagation drives the realtime plan path and asserts baggage is
// resolved onto plans, propagated to child plans, and surfaced as attributes.
func TestPlanBaggagePropagation(t *testing.T) {
	t.Parallel()

	engine, _, _ := newTestEngine(t, baggageDemoConfig())

	var plans []SpanPlan
	engine.planTrace(engine.Topology.Roots[0], nil, -1, time.Now(), 0, nil, nil, &Stats{}, &plans, new(int), DefaultMaxSpansPerTrace, false, false)

	planByOp := map[string]SpanPlan{}
	for _, p := range plans {
		planByOp[p.Operation] = p
	}

	// Every plan carries the full baggage set visible while its span is active.
	assert.Equal(t, map[string]string{"tenant.id": "acme", "session.plan": "enterprise"}, planByOp["checkout"].Baggage)
	assert.Equal(t, map[string]string{"tenant.id": "acme-payments", "session.plan": "enterprise"}, planByOp["charge"].Baggage)
	assert.Equal(t, map[string]string{"tenant.id": "acme-payments", "session.plan": "enterprise"}, planByOp["record"].Baggage)

	// Surfaced attributes match the batch path.
	assert.Equal(t, map[string]string{
		"tenant.id":    "acme-payments",
		"session.plan": "enterprise",
	}, baggageAttrsFromPlan(planByOp["charge"]))
	assert.Empty(t, baggageAttrsFromPlan(planByOp["checkout"]))
}

func baggageAttrsFromPlan(p SpanPlan) map[string]string {
	out := map[string]string{}
	for _, a := range p.Attrs {
		if k, ok := strings.CutPrefix(string(a.Key), baggageAttributePrefix); ok {
			out[k] = a.Value.AsString()
		}
	}
	return out
}
