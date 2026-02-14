// Tests for per-operation simulation state: queue depth, circuit breaker, backpressure
// Validates state transitions, rejection behaviour, and engine integration
package synth

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
)

func TestQueueDepthRejectsAtCapacity(t *testing.T) {
	t.Parallel()

	os := &OperationState{MaxQueueDepth: 2}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	os.Enter()
	os.Enter()

	_, _, rejected, reason := os.Evaluate(0, rng)
	assert.True(t, rejected)
	assert.Equal(t, ReasonQueueFull, reason)
}

func TestQueueDepthAllowsBelowCapacity(t *testing.T) {
	t.Parallel()

	os := &OperationState{MaxQueueDepth: 2}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	os.Enter()

	_, _, rejected, _ := os.Evaluate(0, rng)
	assert.False(t, rejected)
}

func TestQueueDepthExitDecrementsCount(t *testing.T) {
	t.Parallel()

	os := &OperationState{MaxQueueDepth: 1}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	os.Enter()

	_, _, rejected, _ := os.Evaluate(0, rng)
	assert.True(t, rejected, "should reject when at capacity")

	os.Exit(0, 10*time.Millisecond, false)

	_, _, rejected, _ = os.Evaluate(0, rng)
	assert.False(t, rejected, "should allow after exit frees a slot")
}

func TestQueueDepthActiveRequestsFloor(t *testing.T) {
	t.Parallel()

	os := &OperationState{MaxQueueDepth: 5}
	os.Exit(0, time.Millisecond, false)
	assert.Equal(t, 0, os.ActiveRequests, "active requests should not go below zero")
}

func TestCircuitBreakerOpensOnThreshold(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		FailureThreshold: 3,
		WindowDuration:   time.Minute,
		Cooldown:         time.Second,
	}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	assert.Equal(t, CircuitClosed, os.Circuit)

	for i := range 3 {
		os.Exit(time.Duration(i)*100*time.Millisecond, 10*time.Millisecond, true)
	}

	assert.Equal(t, CircuitOpen, os.Circuit)

	_, _, rejected, reason := os.Evaluate(200*time.Millisecond, rng)
	assert.True(t, rejected)
	assert.Equal(t, ReasonCircuitOpen, reason)
}

func TestCircuitBreakerHalfOpenAfterCooldown(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		FailureThreshold: 1,
		WindowDuration:   time.Minute,
		Cooldown:         100 * time.Millisecond,
	}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	os.Exit(0, time.Millisecond, true)
	assert.Equal(t, CircuitOpen, os.Circuit)

	_, _, rejected, _ := os.Evaluate(200*time.Millisecond, rng)
	assert.False(t, rejected, "should allow probe after cooldown")
	assert.Equal(t, CircuitHalfOpen, os.Circuit)
}

func TestCircuitBreakerClosesOnHalfOpenSuccess(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		FailureThreshold: 1,
		WindowDuration:   time.Minute,
		Cooldown:         100 * time.Millisecond,
		Circuit:          CircuitHalfOpen,
	}

	os.Exit(time.Second, 10*time.Millisecond, false)
	assert.Equal(t, CircuitClosed, os.Circuit)
	assert.Empty(t, os.FailureWindow, "failure window should be cleared on close")
}

func TestCircuitBreakerReopensOnHalfOpenFailure(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		FailureThreshold: 1,
		WindowDuration:   time.Minute,
		Cooldown:         100 * time.Millisecond,
		Circuit:          CircuitHalfOpen,
	}

	os.Exit(time.Second, 10*time.Millisecond, true)
	assert.Equal(t, CircuitOpen, os.Circuit)
}

func TestCircuitBreakerWindowPruning(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		FailureThreshold: 3,
		WindowDuration:   100 * time.Millisecond,
		Cooldown:         time.Second,
	}

	os.Exit(0, time.Millisecond, true)
	os.Exit(10*time.Millisecond, time.Millisecond, true)
	assert.Len(t, os.FailureWindow, 2)

	// Failures at elapsed=200ms should prune old ones (window=100ms, cutoff=100ms)
	os.Exit(200*time.Millisecond, time.Millisecond, true)
	assert.Len(t, os.FailureWindow, 1, "old failures outside window should be pruned")
	assert.Equal(t, CircuitClosed, os.Circuit, "should not open: only 1 failure in window")
}

func TestBackpressureActivatesOnHighLatency(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		BackpressureThreshold: 50 * time.Millisecond,
		DurationMultiplier:    3.0,
		ErrorRateAdd:          0.1,
	}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	os.Exit(0, 100*time.Millisecond, false)
	assert.True(t, os.BackpressureActive)

	mult, errAdd, rejected, _ := os.Evaluate(0, rng)
	assert.False(t, rejected)
	assert.Equal(t, 3.0, mult)
	assert.Equal(t, 0.1, errAdd)
}

func TestBackpressureInactiveOnLowLatency(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		BackpressureThreshold: 50 * time.Millisecond,
		DurationMultiplier:    3.0,
		ErrorRateAdd:          0.1,
	}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	os.Exit(0, 10*time.Millisecond, false)
	assert.False(t, os.BackpressureActive)

	mult, errAdd, rejected, _ := os.Evaluate(0, rng)
	assert.False(t, rejected)
	assert.Equal(t, 1.0, mult)
	assert.Equal(t, float64(0), errAdd)
}

func TestBackpressureEWMASmoothing(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		BackpressureThreshold: 50 * time.Millisecond,
		DurationMultiplier:    2.0,
	}

	// First sample: 100ms (above threshold)
	os.Exit(0, 100*time.Millisecond, false)
	assert.True(t, os.BackpressureActive)

	// Multiple low-latency samples should bring EWMA below threshold
	for range 20 {
		os.Exit(0, 10*time.Millisecond, false)
	}
	assert.False(t, os.BackpressureActive, "EWMA should drop below threshold after many low-latency samples")
}

func TestBackpressureMultiplierCapped(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		BackpressureThreshold: 50 * time.Millisecond,
		DurationMultiplier:    999.0, // absurdly high
		BackpressureActive:    true,
	}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	mult, _, _, _ := os.Evaluate(0, rng)
	assert.Equal(t, maxBackpressureMultiplier, mult)
}

func TestBackpressureZeroMultiplierDefaultsToOne(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		BackpressureThreshold: 50 * time.Millisecond,
		DurationMultiplier:    0, // zero
		BackpressureActive:    true,
	}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	mult, _, _, _ := os.Evaluate(0, rng)
	assert.Equal(t, 1.0, mult)
}

func TestNewSimulationStateOnlyTracksConfiguredOps(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "svc",
				Operations: []OperationConfig{
					{Name: "tracked", Duration: "10ms", QueueDepth: 5},
					{Name: "untracked", Duration: "10ms"},
				},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	state := NewSimulationState(topo)
	assert.NotNil(t, state.Get("svc.tracked"))
	assert.Nil(t, state.Get("svc.untracked"))
}

func TestNewSimulationStateNilSafe(t *testing.T) {
	t.Parallel()

	var state *SimulationState
	assert.Nil(t, state.Get("anything"))
}

func TestEngineQueueDepthRejection(t *testing.T) {
	t.Parallel()

	// Queue depth rejection works at the OperationState level, not through
	// the engine's sequential walk (which processes one span at a time).
	// Test the rejection span path directly by pre-filling the queue.
	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:       "op",
				Duration:   "10ms",
				QueueDepth: 1,
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)
	engine.State = NewSimulationState(engine.Topology)

	// Pre-fill the queue so the next request is rejected
	opState := engine.State.Get("svc.op")
	require.NotNil(t, opState)
	opState.Enter()

	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, int64(1), stats.QueueRejections)

	// Verify rejection span attributes
	var foundRejected, foundReason bool
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "synth.rejected" && attr.Value.AsBool() {
			foundRejected = true
		}
		if string(attr.Key) == "synth.rejection_reason" && attr.Value.AsString() == ReasonQueueFull {
			foundReason = true
		}
	}
	assert.True(t, foundRejected, "should have synth.rejected=true attribute")
	assert.True(t, foundReason, "should have synth.rejection_reason=queue_full attribute")

	// Verify it's an error span
	assert.Equal(t, codes.Error, spans[0].Status.Code)
}

func TestEngineCircuitBreakerIntegration(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:      "op",
				Duration:  "1ms",
				ErrorRate: "100%",
				CircuitBreaker: &CircuitBreakerConfig{
					FailureThreshold: 2,
					Window:           "1m",
					Cooldown:         "10s",
				},
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)
	engine.State = NewSimulationState(engine.Topology)

	rootOp := engine.Topology.Roots[0]

	// First two calls should succeed (and fail, triggering circuit)
	for range 2 {
		engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	}

	// Third call should be rejected (circuit is open)
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), time.Second, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	assert.Equal(t, int64(1), stats.CircuitBreakerTrips)

	// Verify the rejection span
	spans := exporter.GetSpans()
	lastSpan := spans[len(spans)-1]
	assert.Equal(t, codes.Error, lastSpan.Status.Code)

	attrMap := make(map[string]string)
	for _, attr := range lastSpan.Attributes {
		attrMap[string(attr.Key)] = attr.Value.AsString()
	}
	assert.Equal(t, "circuit_open", attrMap["synth.rejection_reason"])
}

func TestEngineBackpressureIntegration(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{{
			Name: "svc",
			Operations: []OperationConfig{{
				Name:     "op",
				Duration: "10ms",
				Backpressure: &BackpressureConfig{
					LatencyThreshold:   "5ms",
					DurationMultiplier: 3.0,
					ErrorRateAdd:       "50%",
				},
			}},
		}},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, exporter := newTestEngine(t, cfg)
	engine.State = NewSimulationState(engine.Topology)

	rootOp := engine.Topology.Roots[0]

	// First call: 10ms duration > 5ms threshold → backpressure activates
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans1 := exporter.GetSpans()
	require.Len(t, spans1, 1)
	firstDuration := spans1[0].EndTime.Sub(spans1[0].StartTime)

	// Second call: backpressure should be active, amplifying duration
	exporter.Reset()
	engine.walkTrace(context.Background(), rootOp, time.Now(), time.Second, nil, &Stats{}, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans2 := exporter.GetSpans()
	require.Len(t, spans2, 1)
	secondDuration := spans2[0].EndTime.Sub(spans2[0].StartTime)

	// With 3x multiplier, the second span should be noticeably longer
	assert.Greater(t, secondDuration, firstDuration,
		"backpressure should amplify duration (first=%v, second=%v)", firstDuration, secondDuration)
}

func TestEngineStateNotCreatedWithoutConfig(t *testing.T) {
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
	// No state set — engine.State is nil

	rootOp := engine.Topology.Roots[0]
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, &stats, new(int), DefaultMaxSpansPerTrace)
	require.NoError(t, engine.Provider.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	assert.Len(t, spans, 1, "should work normally without state")
	assert.Equal(t, int64(0), stats.QueueRejections)
	assert.Equal(t, int64(0), stats.CircuitBreakerTrips)
}

func TestCircuitBreakerPriorityOverQueueDepth(t *testing.T) {
	t.Parallel()

	os := &OperationState{
		MaxQueueDepth:    10,
		FailureThreshold: 1,
		WindowDuration:   time.Minute,
		Cooldown:         time.Second,
		Circuit:          CircuitOpen,
		OpenedAt:         0,
	}
	rng := rand.New(rand.NewPCG(1, 0)) //nolint:gosec // deterministic seed for testing

	_, _, rejected, reason := os.Evaluate(100*time.Millisecond, rng)
	assert.True(t, rejected)
	assert.Equal(t, ReasonCircuitOpen, reason, "circuit breaker should take priority over queue depth")
}
