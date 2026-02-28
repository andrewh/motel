// Realtime span emission: replays a []SpanPlan at wall-clock times.
package synth

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// spanEvent is a scheduled Start or End at a wall-clock time.
type spanEvent struct {
	WallTime time.Time
	Index    int
	IsEnd    bool
}

// realtimeStats holds atomic counters accumulated during emission.
// Merged into Stats after the goroutine completes.
type realtimeStats struct {
	Spans  atomic.Int64
	Errors atomic.Int64
}

// liveSpan tracks an in-flight OTel span during emission.
type liveSpan struct {
	Span trace.Span
	Ctx  context.Context
}

// emitTrace replays a planned trace at wall-clock times.
// It runs in its own goroutine. baseSimTime is the earliest span's simulated
// start time; baseWallTime is the corresponding wall-clock time. All events
// are scheduled relative to that offset.
// On context cancellation, all open spans are ended immediately.
func emitTrace(ctx context.Context, plans []SpanPlan, baseSimTime time.Time, baseWallTime time.Time, tracers TracerSource, observers []SpanObserver, rstats *realtimeStats) {
	if len(plans) == 0 {
		return
	}

	events := buildEvents(plans)
	live := make([]liveSpan, len(plans))

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C

	for _, ev := range events {
		wallTarget := baseWallTime.Add(ev.WallTime.Sub(baseSimTime))
		timer.Reset(time.Until(wallTarget))

		select {
		case <-ctx.Done():
			endAllOpen(live, plans, rstats)
			return
		case <-timer.C:
		}

		plan := &plans[ev.Index]

		if !ev.IsEnd {
			var parentCtx context.Context
			if plan.ParentIndex >= 0 {
				parentCtx = live[plan.ParentIndex].Ctx
			} else {
				parentCtx = ctx
			}

			tracer := tracers(plan.Service)
			spanCtx, span := tracer.Start(parentCtx, plan.Operation,
				trace.WithTimestamp(plan.StartTime),
				trace.WithSpanKind(plan.Kind),
				trace.WithAttributes(plan.StartAttrs...),
			)
			if len(plan.Attrs) > 0 {
				span.SetAttributes(plan.Attrs...)
			}
			live[ev.Index] = liveSpan{Span: span, Ctx: spanCtx}
		} else {
			ls := live[ev.Index]
			if ls.Span == nil {
				continue
			}
			finishSpan(ls.Span, plan, observers, rstats)
			live[ev.Index] = liveSpan{}
		}
	}
}

// buildEvents creates a sorted list of Start and End events from span plans.
func buildEvents(plans []SpanPlan) []spanEvent {
	events := make([]spanEvent, 0, len(plans)*2)
	for i := range plans {
		events = append(events,
			spanEvent{WallTime: plans[i].StartTime, Index: i, IsEnd: false},
			spanEvent{WallTime: plans[i].EndTime, Index: i, IsEnd: true},
		)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].WallTime.Equal(events[j].WallTime) {
			if events[i].IsEnd != events[j].IsEnd {
				return !events[i].IsEnd
			}
			if events[i].IsEnd {
				return events[i].Index > events[j].Index
			}
			return events[i].Index < events[j].Index
		}
		return events[i].WallTime.Before(events[j].WallTime)
	})
	return events
}

// finishSpan ends a span, records errors, fires observers, and updates stats.
func finishSpan(span trace.Span, plan *SpanPlan, observers []SpanObserver, rstats *realtimeStats) {
	if plan.IsError {
		if plan.Rejected {
			span.SetStatus(codes.Error, plan.RejectionReason)
			span.RecordError(fmt.Errorf("rejected: %s", plan.RejectionReason), trace.WithTimestamp(plan.EndTime))
		} else {
			span.SetStatus(codes.Error, "synthetic error")
			span.RecordError(fmt.Errorf("synthetic error"), trace.WithTimestamp(plan.EndTime))
		}
		rstats.Errors.Add(1)
	}

	rstats.Spans.Add(1)
	span.End(trace.WithTimestamp(plan.EndTime))

	if len(observers) > 0 {
		info := SpanInfo{
			Service:   plan.Service,
			Operation: plan.Operation,
			Timestamp: plan.StartTime,
			Duration:  plan.EndTime.Sub(plan.StartTime),
			IsError:   plan.IsError,
			Kind:      plan.Kind,
			Attrs:     plan.Attrs,
			Scenarios: plan.Scenarios,
		}
		for _, obs := range observers {
			obs.Observe(info)
		}
	}
}

// endAllOpen ends all open spans on context cancellation.
// Iterates in reverse order so children end before parents.
func endAllOpen(live []liveSpan, plans []SpanPlan, rstats *realtimeStats) {
	now := time.Now()
	for i := len(live) - 1; i >= 0; i-- {
		if live[i].Span == nil {
			continue
		}
		live[i].Span.SetStatus(codes.Error, "cancelled")
		live[i].Span.End(trace.WithTimestamp(now))
		rstats.Spans.Add(1)
		rstats.Errors.Add(1)
	}
}
