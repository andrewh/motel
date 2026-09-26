// Realtime span emission: replays a []SpanPlan at wall-clock times.
package synth

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type spanEventKind int

const (
	spanStart spanEventKind = iota
	spanConfiguredEvent
	spanEnd
)

// spanEvent schedules a span start, configured event, or end at a wall-clock time.
type spanEvent struct {
	SimTime    time.Time
	Index      int
	Kind       spanEventKind
	EventIndex int
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
func emitTrace(ctx context.Context, plans []SpanPlan, baseSimTime time.Time, baseWallTime time.Time, tracers TracerSource, observers []SpanObserver, rstats *realtimeStats, registry *spanContextRegistry) {
	if len(plans) == 0 {
		return
	}

	events := buildEvents(plans)
	live := make([]liveSpan, len(plans))

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C

	for _, ev := range events {
		wallTarget := baseWallTime.Add(ev.SimTime.Sub(baseSimTime))
		timer.Reset(time.Until(wallTarget))

		select {
		case <-ctx.Done():
			endAllOpen(live, plans, observers, rstats)
			return
		case <-timer.C:
		}

		// Cancellation wins even when the next scheduled action is already due.
		// Otherwise select can randomly emit another event after cancellation.
		if ctx.Err() != nil {
			endAllOpen(live, plans, observers, rstats)
			return
		}

		plan := &plans[ev.Index]

		switch ev.Kind {
		case spanStart:
			var parentCtx context.Context
			if plan.ParentIndex >= 0 {
				parentCtx = live[plan.ParentIndex].Ctx
			} else {
				parentCtx = ctx
			}

			// Place the span's baggage on the context so it propagates as real
			// OTel baggage (planTrace already resolved the inherited + declared set).
			if len(plan.Baggage) > 0 {
				parentCtx = baggage.ContextWithBaggage(parentCtx, buildBaggage(plan.Baggage))
			}

			startOpts := []trace.SpanStartOption{
				trace.WithTimestamp(plan.StartTime),
				trace.WithSpanKind(plan.Kind),
				trace.WithAttributes(plan.StartAttrs...),
			}
			if len(plan.LinkRefs) > 0 && registry != nil {
				var links []trace.Link
				for _, lr := range plan.LinkRefs {
					if sc, ok := registry.load(lr.Ref); ok {
						links = append(links, trace.Link{SpanContext: sc, Attributes: lr.Attributes})
					}
				}
				if len(links) > 0 {
					startOpts = append(startOpts, trace.WithLinks(links...))
				}
			}

			tracer := tracers(plan.Service)
			spanCtx, span := tracer.Start(parentCtx, plan.Operation, startOpts...)
			if registry != nil && !plan.Rejected {
				registry.store(plan.Ref, span.SpanContext())
			}
			if len(plan.Attrs) > 0 {
				span.SetAttributes(plan.Attrs...)
			}
			notifySpanStart(observers, plan.Service, plan.Operation)
			live[ev.Index] = liveSpan{Span: span, Ctx: spanCtx}
		case spanConfiguredEvent:
			if span := live[ev.Index].Span; span != nil {
				event := plan.Events[ev.EventIndex]
				span.AddEvent(event.Name, trace.WithTimestamp(event.Timestamp), trace.WithAttributes(event.Attributes...))
			}
		case spanEnd:
			ls := live[ev.Index]
			if ls.Span == nil {
				continue
			}
			finishSpan(ls.Span, plan, plans, observers, rstats)
			live[ev.Index] = liveSpan{}
		}
	}
}

// buildEvents orders starts before configured events before ends at equal times.
// Events after the planned end are omitted because the span is no longer active.
func buildEvents(plans []SpanPlan) []spanEvent {
	events := make([]spanEvent, 0, len(plans)*2)
	for i := range plans {
		events = append(events,
			spanEvent{SimTime: plans[i].StartTime, Index: i, Kind: spanStart},
			spanEvent{SimTime: plans[i].EndTime, Index: i, Kind: spanEnd},
		)
		for j, event := range plans[i].Events {
			if event.Timestamp.After(plans[i].EndTime) {
				continue
			}
			events = append(events, spanEvent{SimTime: event.Timestamp, Index: i, Kind: spanConfiguredEvent, EventIndex: j})
		}
	}
	slices.SortFunc(events, func(a, b spanEvent) int {
		if c := a.SimTime.Compare(b.SimTime); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		// Among ends, higher index first (children end before parents).
		if a.Kind == spanEnd {
			return cmp.Compare(b.Index, a.Index)
		}
		if c := cmp.Compare(a.Index, b.Index); c != 0 {
			return c
		}
		return cmp.Compare(a.EventIndex, b.EventIndex)
	})
	return events
}

// planParentNames returns the parent's service and operation names for a
// plan, or empty strings for root spans.
func planParentNames(plans []SpanPlan, plan *SpanPlan) (string, string) {
	if plan.ParentIndex < 0 {
		return "", ""
	}
	p := &plans[plan.ParentIndex]
	return p.Service, p.Operation
}

// finishSpan ends a span, records errors, fires observers, and updates stats.
func finishSpan(span trace.Span, plan *SpanPlan, plans []SpanPlan, observers []SpanObserver, rstats *realtimeStats) {
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
		parentService, parentOperation := planParentNames(plans, plan)
		info := newSpanInfo(
			plan.Service, plan.Operation,
			parentService, parentOperation,
			plan.StartTime, plan.EndTime.Sub(plan.StartTime),
			plan.IsError, plan.Kind,
			plan.Attrs, plan.Scenarios,
			span.SpanContext(),
		)
		for _, obs := range observers {
			obs.Observe(info)
		}
	}
}

// endAllOpen ends all open spans on context cancellation.
// Iterates in reverse order so children end before parents.
// Fires Observe for each cancelled span to balance updowncounter increments from ObserveStart.
func endAllOpen(live []liveSpan, plans []SpanPlan, observers []SpanObserver, rstats *realtimeStats) {
	now := time.Now()
	for i := len(live) - 1; i >= 0; i-- {
		if live[i].Span == nil {
			continue
		}
		live[i].Span.SetStatus(codes.Error, "cancelled")
		live[i].Span.End(trace.WithTimestamp(now))
		rstats.Spans.Add(1)
		rstats.Errors.Add(1)
		if len(observers) > 0 {
			plan := &plans[i]
			parentService, parentOperation := planParentNames(plans, plan)
			info := newSpanInfo(
				plan.Service, plan.Operation,
				parentService, parentOperation,
				plan.StartTime, now.Sub(plan.StartTime),
				true, plan.Kind,
				plan.Attrs, plan.Scenarios,
				live[i].Span.SpanContext(),
			)
			for _, obs := range observers {
				obs.Observe(info)
			}
		}
	}
}
