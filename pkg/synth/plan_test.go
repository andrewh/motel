// Tests for planTrace: verifies that planned spans match walkTrace output
package synth

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestPlanTraceBasic(t *testing.T) {
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

	engine, _, _ := newTestEngine(t, cfg)

	rootOp := engine.Topology.Roots[0]
	now := time.Now()
	var stats Stats
	var plans []SpanPlan
	spanCount := 0

	engine.Rng = rand.New(rand.NewPCG(42, 0))
	endTime, isError := engine.planTrace(rootOp, -1, now, 0, nil, nil, &stats, &plans, &spanCount, DefaultMaxSpansPerTrace)

	require.Len(t, plans, 2)

	root := plans[0]
	child := plans[1]

	assert.Equal(t, "gateway", root.Service)
	assert.Equal(t, "GET /users", root.Operation)
	assert.Equal(t, -1, root.ParentIndex)
	assert.Equal(t, 0, root.Index)
	assert.Equal(t, trace.SpanKindServer, root.Kind)

	assert.Equal(t, "backend", child.Service)
	assert.Equal(t, "list", child.Operation)
	assert.Equal(t, 0, child.ParentIndex)
	assert.Equal(t, 1, child.Index)
	assert.Equal(t, trace.SpanKindClient, child.Kind)

	assert.False(t, child.StartTime.Before(root.StartTime))
	assert.False(t, root.EndTime.Before(child.EndTime))
	assert.Equal(t, endTime, root.EndTime)
	assert.Equal(t, root.IsError, isError)
}

func TestPlanTraceMatchesWalkTrace(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:      "GET /users",
					Duration:  "30ms +/- 10ms",
					ErrorRate: "0.1",
					Calls: []CallConfig{
						{Target: "backend.list"},
						{Target: "cache.lookup"},
					},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "list",
					Duration: "20ms +/- 5ms",
					Calls:    []CallConfig{{Target: "db.query"}},
				}},
			},
			{
				Name: "cache",
				Operations: []OperationConfig{{
					Name:     "lookup",
					Duration: "2ms +/- 1ms",
				}},
			},
			{
				Name: "db",
				Operations: []OperationConfig{{
					Name:     "query",
					Duration: "10ms +/- 3ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	seed := [2]uint64{99, 7}
	now := time.Now()

	// Run planTrace
	planEngine, _, _ := newTestEngine(t, cfg)
	planEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var planStats Stats
	var plans []SpanPlan
	planSpanCount := 0
	planEnd, planErr := planEngine.planTrace(
		planEngine.Topology.Roots[0], -1, now, 0, nil, nil,
		&planStats, &plans, &planSpanCount, DefaultMaxSpansPerTrace,
	)

	// Run walkTrace with the same seed
	walkEngine, exporter, tp := newTestEngine(t, cfg)
	walkEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var walkStats Stats
	walkSpanCount := 0
	walkEnd, walkErr := walkEngine.walkTrace(
		context.Background(), walkEngine.Topology.Roots[0], now, 0, nil, nil,
		&walkStats, &walkSpanCount, DefaultMaxSpansPerTrace,
	)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()

	assert.Equal(t, walkEnd, planEnd, "end times must match")
	assert.Equal(t, walkErr, planErr, "error states must match")
	require.Len(t, plans, len(spans), "plan count must match span count")

	// The OTel exporter records spans in End order (post-order), while planTrace
	// appends in Start order (pre-order). Match by start time instead of index.
	type spanKey struct {
		Name      string
		StartTime time.Time
	}
	spanByKey := make(map[spanKey]int, len(spans))
	for i, s := range spans {
		spanByKey[spanKey{s.Name, s.StartTime}] = i
	}
	for _, plan := range plans {
		key := spanKey{plan.Operation, plan.StartTime}
		si, ok := spanByKey[key]
		require.True(t, ok, "plan %s@%v not found in exporter spans", plan.Operation, plan.StartTime)
		s := spans[si]
		assert.Equal(t, s.EndTime, plan.EndTime, "%s end time", plan.Operation)
		assert.Equal(t, s.SpanKind, plan.Kind, "%s kind", plan.Operation)
	}
}

func TestPlanTraceSpanLimit(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "GET /users",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "backend.op"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "5ms",
					Calls:    []CallConfig{{Target: "db.query"}},
				}},
			},
			{
				Name: "db",
				Operations: []OperationConfig{{
					Name:     "query",
					Duration: "2ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	engine, _, _ := newTestEngine(t, cfg)
	var stats Stats
	var plans []SpanPlan
	spanCount := 0

	engine.planTrace(engine.Topology.Roots[0], -1, time.Now(), 0, nil, nil,
		&stats, &plans, &spanCount, 2)

	assert.Equal(t, 2, len(plans), "should stop at span limit")
}

func TestPlanTraceRejection(t *testing.T) {
	t.Parallel()

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

	engine, _, _ := newTestEngine(t, cfg)
	engine.State = NewSimulationState(engine.Topology)

	rootOp := engine.Topology.Roots[0]

	// First call succeeds and fills the queue
	var stats1 Stats
	var plans1 []SpanPlan
	sc1 := 0
	engine.planTrace(rootOp, -1, time.Now(), 0, nil, nil, &stats1, &plans1, &sc1, DefaultMaxSpansPerTrace)

	// Manually bump active requests to trigger queue full
	opState := engine.State.Get(rootOp.Ref)
	opState.ActiveRequests = 1

	var stats2 Stats
	var plans2 []SpanPlan
	sc2 := 0
	engine.planTrace(rootOp, -1, time.Now(), time.Second, nil, nil, &stats2, &plans2, &sc2, DefaultMaxSpansPerTrace)

	require.Len(t, plans2, 1)
	assert.True(t, plans2[0].Rejected)
	assert.Equal(t, ReasonQueueFull, plans2[0].RejectionReason)
	assert.True(t, plans2[0].IsError)
	assert.Equal(t, int64(1), stats2.QueueRejections)
}

func TestPlanTraceSequentialCalls(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:      "request",
					Duration:  "10ms",
					CallStyle: "sequential",
					Calls: []CallConfig{
						{Target: "svc-a.op"},
						{Target: "svc-b.op"},
					},
				}},
			},
			{Name: "svc-a", Operations: []OperationConfig{{Name: "op", Duration: "5ms"}}},
			{Name: "svc-b", Operations: []OperationConfig{{Name: "op", Duration: "5ms"}}},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	seed := [2]uint64{123, 456}
	now := time.Now()

	// Plan path
	planEngine, _, _ := newTestEngine(t, cfg)
	planEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var planStats Stats
	var plans []SpanPlan
	psc := 0
	planEngine.planTrace(planEngine.Topology.Roots[0], -1, now, 0, nil, nil,
		&planStats, &plans, &psc, DefaultMaxSpansPerTrace)

	// Walk path with same seed
	walkEngine, exporter, tp := newTestEngine(t, cfg)
	walkEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var walkStats Stats
	wsc := 0
	walkEngine.walkTrace(context.Background(), walkEngine.Topology.Roots[0], now, 0, nil, nil,
		&walkStats, &wsc, DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, plans, len(spans))

	// In sequential mode, svc-b should start after svc-a ends
	var svcA, svcB *SpanPlan
	for i := range plans {
		if plans[i].Service == "svc-a" {
			svcA = &plans[i]
		}
		if plans[i].Service == "svc-b" {
			svcB = &plans[i]
		}
	}
	require.NotNil(t, svcA)
	require.NotNil(t, svcB)
	assert.False(t, svcB.StartTime.Before(svcA.EndTime),
		"sequential: svc-b should start at or after svc-a ends")
}

func TestPlanTraceRetries(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "caller",
				Operations: []OperationConfig{{
					Name:     "request",
					Duration: "10ms",
					Calls: []CallConfig{{
						Target:       "callee.op",
						Retries:      2,
						RetryBackoff: "5ms",
					}},
				}},
			},
			{
				Name: "callee",
				Operations: []OperationConfig{{
					Name:      "op",
					Duration:  "5ms",
					ErrorRate: "1.0",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}

	seed := [2]uint64{77, 88}
	now := time.Now()

	// Plan
	planEngine, _, _ := newTestEngine(t, cfg)
	planEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var planStats Stats
	var plans []SpanPlan
	psc := 0
	planEngine.planTrace(planEngine.Topology.Roots[0], -1, now, 0, nil, nil,
		&planStats, &plans, &psc, DefaultMaxSpansPerTrace)

	// Walk
	walkEngine, exporter, tp := newTestEngine(t, cfg)
	walkEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var walkStats Stats
	wsc := 0
	walkEngine.walkTrace(context.Background(), walkEngine.Topology.Roots[0], now, 0, nil, nil,
		&walkStats, &wsc, DefaultMaxSpansPerTrace)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, plans, len(spans), "should have same span count with retries")
	assert.Equal(t, walkStats.Retries, planStats.Retries, "retry counts must match")
}
