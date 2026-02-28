// Tests for realtime span emission
package synth

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.False(t, events[0].IsEnd)
	assert.Equal(t, 0, events[0].Index)

	assert.False(t, events[1].IsEnd)
	assert.Equal(t, 1, events[1].Index)

	assert.True(t, events[2].IsEnd)
	assert.Equal(t, 1, events[2].Index)

	assert.True(t, events[3].IsEnd)
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
	assert.False(t, events[0].IsEnd)
	assert.Equal(t, 0, events[0].Index)
	assert.False(t, events[1].IsEnd)
	assert.Equal(t, 1, events[1].Index)

	// Ends: higher index first (children end before parents)
	assert.True(t, events[2].IsEnd)
	assert.Equal(t, 1, events[2].Index)
	assert.True(t, events[3].IsEnd)
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
	emitTrace(context.Background(), plans, now, time.Now(), tracers, nil, &rstats)

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
	emitTrace(context.Background(), plans, now, time.Now(), tracers, nil, &rstats)

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
	emitTrace(context.Background(), plans, now, time.Now(), tracers, nil, &rstats)

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
	emitTrace(ctx, plans, now, time.Now(), tracers, nil, &rstats)

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
	emitTrace(context.Background(), nil, time.Now(), time.Now(), nil, nil, &rstats)
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
	emitTrace(context.Background(), plans, now, time.Now(), tracers, []SpanObserver{obs}, &rstats)

	require.Len(t, observed, 1)
	assert.Equal(t, "gateway", observed[0].Service)
	assert.Equal(t, "GET /users", observed[0].Operation)
}

type observerFunc func(SpanInfo)

func (f observerFunc) Observe(info SpanInfo) { f(info) }
