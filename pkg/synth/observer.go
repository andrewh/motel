// SpanObserver interface for deriving signals (metrics, logs) from emitted spans.
// Observers receive span metadata after each span completes.
package synth

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SpanInfo holds span metadata for signal derivation.
type SpanInfo struct {
	Service   string
	Operation string
	Timestamp time.Time
	Duration  time.Duration
	IsError   bool
	Kind      trace.SpanKind
	Attrs     []attribute.KeyValue
	Scenarios []string
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
func newSpanInfo(service, operation string, timestamp time.Time, duration time.Duration, isError bool, kind trace.SpanKind, attrs []attribute.KeyValue, scenarios []string) SpanInfo {
	return SpanInfo{
		Service:   service,
		Operation: operation,
		Timestamp: timestamp,
		Duration:  duration,
		IsError:   isError,
		Kind:      kind,
		Attrs:     attrs,
		Scenarios: scenarios,
	}
}
