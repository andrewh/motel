// Tests for realtime span emission
package synth

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestBuildEvents(t *testing.T) {
	t.Parallel()

	now := time.Now()
	plans := []SpanPlan{
		{Index: 0, StartTime: now, EndTime: now.Add(100 * time.Millisecond)},
		{Index: 1, StartTime: now.Add(25 * time.Millisecond), EndTime: now.Add(75 * time.Millisecond)},
	}

	events := buildEvents(plans)
	require.Len(t, events, 4)

	// Verify sorted order: start0, start1, end1, end0
	assert.Equal(t, spanStart, events[0].Kind)
	assert.Equal(t, 0, events[0].Index)

	assert.Equal(t, spanStart, events[1].Kind)
	assert.Equal(t, 1, events[1].Index)

	assert.Equal(t, spanEnd, events[2].Kind)
	assert.Equal(t, 1, events[2].Index)

	assert.Equal(t, spanEnd, events[3].Kind)
	assert.Equal(t, 0, events[3].Index)
}

func TestBuildEventsSimultaneous(t *testing.T) {
	t.Parallel()

	now := time.Now()
	plans := []SpanPlan{
		{Index: 0, StartTime: now, EndTime: now.Add(10 * time.Millisecond)},
		{Index: 1, StartTime: now, EndTime: now.Add(10 * time.Millisecond)},
	}

	events := buildEvents(plans)
	require.Len(t, events, 4)

	// At same time: starts before ends, lower index first for starts
	assert.Equal(t, spanStart, events[0].Kind)
	assert.Equal(t, 0, events[0].Index)
	assert.Equal(t, spanStart, events[1].Kind)
	assert.Equal(t, 1, events[1].Index)

	// Ends: higher index first (children end before parents)
	assert.Equal(t, spanEnd, events[2].Kind)
	assert.Equal(t, 1, events[2].Index)
	assert.Equal(t, spanEnd, events[3].Kind)
	assert.Equal(t, 0, events[3].Index)
}

func TestEmitTraceProducesSpans(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracers := func(name string) trace.Tracer { return tp.Tracer(name) }

	now := time.Now()
	plans := []SpanPlan{
		{
			Index:       0,
			ParentIndex: -1,
			Service:     "gateway",
			Operation:   "GET /users",
			Kind:        trace.SpanKindServer,
			StartTime:   now,
			EndTime:     now.Add(50 * time.Millisecond),
		},
		{
			Index:       1,
			ParentIndex: 0,
			Service:     "backend",
			Operation:   "list",
			Kind:        trace.SpanKindClient,
			StartTime:   now.Add(10 * time.Millisecond),
			EndTime:     now.Add(40 * time.Millisecond),
		},
	}

	var rstats realtimeStats
	emitTrace(context.Background(), plans, now, time.Now(), tracers, nil, &rstats, nil)

	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)

	assert.Equal(t, int64(2), rstats.Spans.Load())
	assert.Equal(t, int64(0), rstats.Errors.Load())

	// Verify parent-child relationship
	slices.SortFunc(spans, func(a, b tracetest.SpanStub) int {
		return a.StartTime.Compare(b.StartTime)
	})

	root := spans[0]
	child := spans[1]
	assert.Equal(t, "GET /users", root.Name)
	assert.Equal(t, "list", child.Name)
	assert.Equal(t, root.SpanContext.SpanID(), child.Parent.SpanID())
	assert.Equal(t, root.SpanContext.TraceID(), child.SpanContext.TraceID())
}

func TestEmitTraceSpanLinkAttributes(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracers := func(name string) trace.Tracer { return tp.Tracer(name) }

	now := time.Now()
	plans := []SpanPlan{
		{
			Index:       0,
			ParentIndex: -1,
			Ref:         "producer.enqueue",
			Service:     "producer",
			Operation:   "enqueue",
			Kind:        trace.SpanKindServer,
			StartTime:   now,
			EndTime:     now.Add(20 * time.Millisecond),
		},
		{
			Index:       1,
			ParentIndex: -1,
			Ref:         "consumer.dequeue",
			Service:     "consumer",
			Operation:   "dequeue",
			Kind:        trace.SpanKindServer,
			StartTime:   now.Add(5 * time.Millisecond),
			EndTime:     now.Add(15 * time.Millisecond),
			LinkRefs: []LinkRef{{
				Ref: "producer.enqueue",
				Attributes: []attribute.KeyValue{
					attribute.String("messaging.message.id", "msg-42"),
					attribute.Int("messaging.batch.message.index", 7),
				},
			}},
		},
	}

	registry := &spanContextRegistry{
		ctx:     make(map[string]trace.SpanContext),
		targets: map[string]bool{"producer.enqueue": true},
	}

	var rstats realtimeStats
	emitTrace(context.Background(), plans, now, time.Now(), tracers, nil, &rstats, registry)

	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)

	byName := make(map[string]tracetest.SpanStub, len(spans))
	for _, span := range spans {
		byName[span.Name] = span
	}
	consumer := byName["dequeue"]
	producer := byName["enqueue"]
	require.Len(t, consumer.Links, 1)
	assert.Equal(t, producer.SpanContext.SpanID(), consumer.Links[0].SpanContext.SpanID())

	linkAttrs := make(map[string]attribute.Value)
	for _, kv := range consumer.Links[0].Attributes {
		linkAttrs[string(kv.Key)] = kv.Value
	}
	assert.Equal(t, attribute.StringValue("msg-42"), linkAttrs["messaging.message.id"])
	assert.Equal(t, attribute.IntValue(7), linkAttrs["messaging.batch.message.index"])
}

func TestEmitTraceErrors(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracers := func(name string) trace.Tracer { return tp.Tracer(name) }

	now := time.Now()
	plans := []SpanPlan{
		{
			Index:       0,
			ParentIndex: -1,
			Service:     "svc",
			Operation:   "op",
			Kind:        trace.SpanKindServer,
			StartTime:   now,
			EndTime:     now.Add(10 * time.Millisecond),
			IsError:     true,
		},
	}

	var rstats realtimeStats
	emitTrace(context.Background(), plans, now, time.Now(), tracers, nil, &rstats, nil)

	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	assert.Equal(t, int64(1), rstats.Errors.Load())
}

func TestEmitTraceRejection(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracers := func(name string) trace.Tracer { return tp.Tracer(name) }

	now := time.Now()
	plans := []SpanPlan{
		{
			Index:           0,
			ParentIndex:     -1,
			Service:         "svc",
			Operation:       "op",
			Kind:            trace.SpanKindServer,
			StartTime:       now,
			EndTime:         now.Add(time.Millisecond),
			IsError:         true,
			Rejected:        true,
			RejectionReason: ReasonQueueFull,
		},
	}

	var rstats realtimeStats
	emitTrace(context.Background(), plans, now, time.Now(), tracers, nil, &rstats, nil)

	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	assert.Equal(t, ReasonQueueFull, spans[0].Status.Description)
}

func TestEmitTraceCancellation(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracers := func(name string) trace.Tracer { return tp.Tracer(name) }

	now := time.Now()
	plans := []SpanPlan{
		{
			Index:       0,
			ParentIndex: -1,
			Service:     "svc",
			Operation:   "op",
			Kind:        trace.SpanKindServer,
			StartTime:   now,
			EndTime:     now.Add(10 * time.Second),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so the Start event fires but End is far in the future.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	var rstats realtimeStats
	emitTrace(ctx, plans, now, time.Now(), tracers, nil, &rstats, nil)

	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1, "span should be ended on cancellation")
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	assert.Equal(t, "cancelled", spans[0].Status.Description)
	assert.Equal(t, int64(1), rstats.Spans.Load())
}

func TestEmitTraceEmpty(t *testing.T) {
	t.Parallel()

	var rstats realtimeStats
	emitTrace(context.Background(), nil, time.Now(), time.Now(), nil, nil, &rstats, nil)
	assert.Equal(t, int64(0), rstats.Spans.Load())
}

func TestEmitTraceObservers(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracers := func(name string) trace.Tracer { return tp.Tracer(name) }

	var observed []SpanInfo
	obs := observerFunc(func(info SpanInfo) {
		observed = append(observed, info)
	})

	now := time.Now()
	plans := []SpanPlan{
		{
			Index:       0,
			ParentIndex: -1,
			Service:     "gateway",
			Operation:   "GET /users",
			Kind:        trace.SpanKindServer,
			StartTime:   now,
			EndTime:     now.Add(30 * time.Millisecond),
		},
	}

	var rstats realtimeStats
	emitTrace(context.Background(), plans, now, time.Now(), tracers, []SpanObserver{obs}, &rstats, nil)

	require.Len(t, observed, 1)
	assert.Equal(t, "gateway", observed[0].Service)
	assert.Equal(t, "GET /users", observed[0].Operation)
}

type observerFunc func(SpanInfo)

func (f observerFunc) Observe(info SpanInfo) { f(info) }

type cancelOnOperationStart struct {
	operation string
	cancel    context.CancelFunc
}

func (o cancelOnOperationStart) Observe(SpanInfo) {}

func (o cancelOnOperationStart) ObserveStart(_, operation string) {
	if operation == o.operation {
		o.cancel()
	}
}

func TestEmitTraceCancellationPreservesOnlyReachedEvents(t *testing.T) {
	for _, afterEvent := range []bool{false, true} {
		t.Run(map[bool]string{false: "before event", true: "after event"}[afterEvent], func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := time.Now()
			const eventDelay = time.Millisecond
			const spanDuration = time.Hour
			plans := []SpanPlan{{
				Index: 0, ParentIndex: -1, Service: "svc", Operation: "root",
				StartTime: start, EndTime: start.Add(spanDuration),
				Events: []EventPlan{
					{Name: "first", Timestamp: start.Add(eventDelay), Attributes: []attribute.KeyValue{attribute.String("key", "value")}},
					{Name: "future", Timestamp: start.Add(spanDuration / 2)},
				},
			}}
			cancelOperation := "root"
			if afterEvent {
				cancelOperation = "cancel"
				plans = append(plans, SpanPlan{
					Index: 1, ParentIndex: 0, Service: "svc", Operation: cancelOperation,
					StartTime: start.Add(2 * eventDelay), EndTime: start.Add(spanDuration),
				})
			}
			tracers := func(name string) trace.Tracer { return tp.Tracer(name) }
			emitTrace(ctx, plans, start, start, tracers, []SpanObserver{cancelOnOperationStart{cancelOperation, cancel}}, &realtimeStats{}, nil)
			spans := exporter.GetSpans()
			require.Len(t, spans, len(plans))
			root := spans[len(spans)-1]
			require.Equal(t, "root", root.Name)
			assert.Equal(t, "cancelled", root.Status.Description)
			if afterEvent {
				require.Len(t, root.Events, 1)
				assert.Equal(t, "first", root.Events[0].Name)
				assert.Equal(t, start.Add(eventDelay), root.Events[0].Time)
				assert.Equal(t, plans[0].Events[0].Attributes, root.Events[0].Attributes)
			} else {
				assert.Empty(t, root.Events)
			}
		})
	}
}

func TestEmitTraceEventsAtSpanBoundaries(t *testing.T) {
	for _, realtime := range []bool{false, true} {
		mode := "instant"
		if realtime {
			mode = "realtime"
		}
		t.Run(mode, func(t *testing.T) {
			for _, duration := range []time.Duration{0, time.Millisecond} {
				t.Run(duration.String(), func(t *testing.T) {
					exporter := tracetest.NewInMemoryExporter()
					tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
					t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
					start := time.Now()
					end := start.Add(duration)
					plans := []SpanPlan{{
						Index: 0, ParentIndex: -1, Service: "svc", Operation: "op",
						StartTime: start, EndTime: end,
						Events: []EventPlan{
							{Name: "start", Timestamp: start},
							{Name: "end-first", Timestamp: end},
							{Name: "end-second", Timestamp: end},
							{Name: "outside", Timestamp: end.Add(time.Hour)},
						},
					}}
					tracers := func(name string) trace.Tracer { return tp.Tracer(name) }
					if realtime {
						emitTrace(context.Background(), plans, start, start, tracers, nil, &realtimeStats{}, nil)
					} else {
						emitTraceInstant(plans, tracers, nil, &realtimeStats{})
					}
					spans := exporter.GetSpans()
					require.Len(t, spans, 1)
					require.Len(t, spans[0].Events, 3)
					for i, want := range plans[0].Events[:3] {
						assert.Equal(t, want.Name, spans[0].Events[i].Name)
						assert.Equal(t, want.Timestamp, spans[0].Events[i].Time)
					}
				})
			}
		})
	}
}
