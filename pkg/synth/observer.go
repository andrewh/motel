// SpanObserver interface for deriving signals (metrics, logs) from emitted spans.
// Observers receive span metadata after each span completes.
package synth

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SpanInfo holds span metadata for signal derivation.
// ParentService and ParentOperation identify the calling operation;
// both are empty for root spans.
type SpanInfo struct {
	Service         string
	Operation       string
	ParentService   string
	ParentOperation string
	Timestamp       time.Time
	Duration        time.Duration
	IsError         bool
	Kind            trace.SpanKind
	Attrs           []attribute.KeyValue
	Scenarios       []string
	SpanContext     trace.SpanContext
}

// SpanObserver receives span metadata after each span is emitted.
type SpanObserver interface {
	Observe(info SpanInfo)
}

// SpanStartObserver receives notification when a span starts.
// Observers that need to track active spans (e.g. updowncounter) implement this.
type SpanStartObserver interface {
	ObserveStart(service, operation string)
}

// notifySpanStart dispatches ObserveStart to all observers that implement SpanStartObserver.
func notifySpanStart(observers []SpanObserver, service, operation string) {
	for _, obs := range observers {
		if sso, ok := obs.(SpanStartObserver); ok {
			sso.ObserveStart(service, operation)
		}
	}
}

// Plan event kinds reported to PlanEventObserver.
const (
	PlanEventTimeout            = "timeout"
	PlanEventRetry              = "retry"
	PlanEventQueueRejection     = "queue_rejection"
	PlanEventCircuitBreakerTrip = "circuit_breaker_trip"
)

// PlanEvent describes a plan-phase decision made during trace generation.
// These decisions (timeouts, retries, queue rejections, circuit breaker
// trips) are simulation ground truth that does not appear as distinct
// records in the emitted telemetry.
type PlanEvent struct {
	Kind      string
	Service   string
	Operation string
	Timestamp time.Time
}

// PlanEventObserver receives plan-phase decisions as they are made.
// Observers that record or display simulation ground truth implement this.
type PlanEventObserver interface {
	ObservePlanEvent(ev PlanEvent)
}

// notifyPlanEvent dispatches a PlanEvent to all observers that implement PlanEventObserver.
func notifyPlanEvent(observers []SpanObserver, ev PlanEvent) {
	for _, obs := range observers {
		if peo, ok := obs.(PlanEventObserver); ok {
			peo.ObservePlanEvent(ev)
		}
	}
}

// OverrideObserver receives the currently active scenario overrides.
// Observers whose output depends on scenario state (e.g. metric value
// overrides) implement this. The engine calls SetOverrides with the merged
// overrides for the current simulation time; a nil map means no scenario
// is active.
type OverrideObserver interface {
	SetOverrides(overrides map[string]Override)
}

// notifyOverrides dispatches SetOverrides to all observers that implement OverrideObserver.
func notifyOverrides(observers []SpanObserver, overrides map[string]Override) {
	for _, obs := range observers {
		if oo, ok := obs.(OverrideObserver); ok {
			oo.SetOverrides(overrides)
		}
	}
}

// newSpanInfo constructs a SpanInfo from its component fields.
// parentService and parentOperation are empty for root spans.
func newSpanInfo(service, operation, parentService, parentOperation string, timestamp time.Time, duration time.Duration, isError bool, kind trace.SpanKind, attrs []attribute.KeyValue, scenarios []string, spanCtx trace.SpanContext) SpanInfo {
	return SpanInfo{
		Service:         service,
		Operation:       operation,
		ParentService:   parentService,
		ParentOperation: parentOperation,
		Timestamp:       timestamp,
		Duration:        duration,
		IsError:         isError,
		Kind:            kind,
		Attrs:           attrs,
		Scenarios:       scenarios,
		SpanContext:     spanCtx,
	}
}
