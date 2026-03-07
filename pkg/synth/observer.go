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
