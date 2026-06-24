// Tests for planTrace: verifies that planned spans match walkTrace output
package synth

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
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
	endTime, isError := engine.planTrace(rootOp, -1, now, 0, nil, nil, &stats, &plans, &spanCount, DefaultMaxSpansPerTrace, false)

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
		&planStats, &plans, &planSpanCount, DefaultMaxSpansPerTrace, false,
	)

	// Run walkTrace with the same seed
	walkEngine, exporter, tp := newTestEngine(t, cfg)
	walkEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var walkStats Stats
	walkSpanCount := 0
	walkEnd, walkErr := walkEngine.walkTrace(
		context.Background(), walkEngine.Topology.Roots[0], nil, now, 0, nil, nil,
		&walkStats, &walkSpanCount, DefaultMaxSpansPerTrace, false,
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
		&stats, &plans, &spanCount, 2, false)

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
	engine.planTrace(rootOp, -1, time.Now(), 0, nil, nil, &stats1, &plans1, &sc1, DefaultMaxSpansPerTrace, false)

	// Manually bump active requests to trigger queue full
	opState := engine.State.Get(rootOp.Ref)
	opState.ActiveRequests = 1

	var stats2 Stats
	var plans2 []SpanPlan
	sc2 := 0
	engine.planTrace(rootOp, -1, time.Now(), time.Second, nil, nil, &stats2, &plans2, &sc2, DefaultMaxSpansPerTrace, false)

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
		&planStats, &plans, &psc, DefaultMaxSpansPerTrace, false)

	// Walk path with same seed
	walkEngine, exporter, tp := newTestEngine(t, cfg)
	walkEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var walkStats Stats
	wsc := 0
	walkEngine.walkTrace(context.Background(), walkEngine.Topology.Roots[0], nil, now, 0, nil, nil,
		&walkStats, &wsc, DefaultMaxSpansPerTrace, false)
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
		&planStats, &plans, &psc, DefaultMaxSpansPerTrace, false)

	// Walk
	walkEngine, exporter, tp := newTestEngine(t, cfg)
	walkEngine.Rng = rand.New(rand.NewPCG(seed[0], seed[1]))
	var walkStats Stats
	wsc := 0
	walkEngine.walkTrace(context.Background(), walkEngine.Topology.Roots[0], nil, now, 0, nil, nil,
		&walkStats, &wsc, DefaultMaxSpansPerTrace, false)
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, plans, len(spans), "should have same span count with retries")
	assert.Equal(t, walkStats.Retries, planStats.Retries, "retry counts must match")
}

func TestPlanTraceSpanLinkAttributes(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "producer",
				Operations: []OperationConfig{{
					Name:     "enqueue",
					Duration: "5ms",
				}},
			},
			{
				Name: "consumer",
				Operations: []OperationConfig{{
					Name:     "dequeue",
					Duration: "10ms",
					Links: []LinkConfig{{
						Ref: "producer.enqueue",
						Attributes: map[string]AttributeValueConfig{
							"messaging.message.id":          {Value: "msg-42"},
							"messaging.batch.message.index": {Value: 7},
						},
					}},
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "10/s"},
	}
	engine, _, _ := newTestEngine(t, cfg)

	var consumerRoot *Operation
	for _, r := range engine.Topology.Roots {
		if r.Ref == "consumer.dequeue" {
			consumerRoot = r
		}
	}
	require.NotNil(t, consumerRoot)

	var plans []SpanPlan
	engine.planTrace(consumerRoot, -1, time.Now(), 0, nil, nil, &Stats{}, &plans, new(int), DefaultMaxSpansPerTrace, false)

	require.Len(t, plans, 1)
	require.Len(t, plans[0].LinkRefs, 1)
	assert.Equal(t, "producer.enqueue", plans[0].LinkRefs[0].Ref)

	linkAttrs := make(map[string]attribute.Value)
	for _, kv := range plans[0].LinkRefs[0].Attributes {
		linkAttrs[string(kv.Key)] = kv.Value
	}
	assert.Equal(t, attribute.StringValue("msg-42"), linkAttrs["messaging.message.id"])
	assert.Equal(t, attribute.IntValue(7), linkAttrs["messaging.batch.message.index"])
}

// twoTierStateConfig builds a gateway->backend topology where backend has a
// queue depth of 1, so pre-filling its state forces a queue rejection.
func twoTierQueueConfig() *Config {
	return &Config{
		Services: []ServiceConfig{
			{
				Name: "gateway",
				Operations: []OperationConfig{{
					Name:     "request",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "backend.handle"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:       "handle",
					Duration:   "5ms",
					QueueDepth: 1,
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "10/s"},
	}
}

// TestRejectionSpanCountedOnce pins that a rejected operation consumes
// exactly one slot of the per-trace span limit on both engine paths.
// Previously the rejection helpers incremented spanCount a second time.
func TestRejectionSpanCountedOnce(t *testing.T) {
	t.Parallel()

	t.Run("walk path", func(t *testing.T) {
		t.Parallel()
		engine, exporter, tp := newTestEngine(t, twoTierQueueConfig())
		engine.State = NewSimulationState(engine.Topology)
		engine.State.Get("backend.handle").Enter() // queue now full

		spanCount := 0
		engine.walkTrace(context.Background(), engine.Topology.Roots[0], nil, time.Now(), 0, nil, nil, &Stats{}, &spanCount, DefaultMaxSpansPerTrace, false)
		require.NoError(t, tp.ForceFlush(context.Background()))

		assert.Equal(t, 2, spanCount, "root span plus one rejected span")
		assert.Len(t, exporter.GetSpans(), 2)
	})

	t.Run("plan path", func(t *testing.T) {
		t.Parallel()
		engine, _, _ := newTestEngine(t, twoTierQueueConfig())
		engine.State = NewSimulationState(engine.Topology)
		engine.State.Get("backend.handle").Enter()

		spanCount := 0
		var plans []SpanPlan
		engine.planTrace(engine.Topology.Roots[0], -1, time.Now(), 0, nil, nil, &Stats{}, &plans, &spanCount, DefaultMaxSpansPerTrace, false)

		assert.Equal(t, 2, spanCount, "root span plus one rejected span")
		assert.Len(t, plans, 2, "span count matches planned spans")
	})
}

// TestPlanTraceAsyncConsumerKind pins that realtime planning marks async
// callees as CONSUMER spans, mirroring walkTrace.
func TestPlanTraceAsyncConsumerKind(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Services: []ServiceConfig{
			{
				Name: "api",
				Operations: []OperationConfig{{
					Name:     "submit",
					Duration: "10ms",
					Calls:    []CallConfig{{Target: "queue.publish", Async: true}},
				}},
			},
			{
				Name: "queue",
				Operations: []OperationConfig{{
					Name:     "publish",
					Duration: "2ms",
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "10/s"},
	}
	engine, _, _ := newTestEngine(t, cfg)

	var plans []SpanPlan
	engine.planTrace(engine.Topology.Roots[0], -1, time.Now(), 0, nil, nil, &Stats{}, &plans, new(int), DefaultMaxSpansPerTrace, false)

	require.Len(t, plans, 2)
	byOp := map[string]SpanPlan{}
	for _, p := range plans {
		byOp[p.Operation] = p
	}
	assert.Equal(t, trace.SpanKindServer, byOp["submit"].Kind)
	assert.Equal(t, trace.SpanKindConsumer, byOp["publish"].Kind, "async callee is a CONSUMER span in realtime mode")
}
