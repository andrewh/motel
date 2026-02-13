// Tests for LogObserver that derives log records from error and slow spans.
// Uses an in-memory log exporter to capture and verify emitted records.
package synth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

type memoryLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memoryLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}

func (e *memoryLogExporter) Shutdown(context.Context) error   { return nil }
func (e *memoryLogExporter) ForceFlush(context.Context) error { return nil }

func (e *memoryLogExporter) get() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]sdklog.Record, len(e.records))
	copy(out, e.records)
	return out
}

func newTestLogObserver(t *testing.T, slowThreshold time.Duration) (*LogObserver, *memoryLogExporter) {
	t.Helper()
	exporter := &memoryLogExporter{}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)),
	)
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	return NewLogObserver(lp, slowThreshold), exporter
}

func TestLogObserverErrorSpan(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, 0)

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  10 * time.Millisecond,
		IsError:   true,
		Kind:      trace.SpanKindServer,
	})

	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t, otellog.SeverityError, records[0].Severity())
	assert.Contains(t, records[0].Body().AsString(), "svc")
	assert.Contains(t, records[0].Body().AsString(), "op")
}

func TestLogObserverSlowSpan(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, 100*time.Millisecond)

	obs.Observe(SpanInfo{
		Service:   "backend",
		Operation: "query",
		Duration:  200 * time.Millisecond,
		IsError:   false,
		Kind:      trace.SpanKindClient,
	})

	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t, otellog.SeverityWarn, records[0].Severity())
	assert.Contains(t, records[0].Body().AsString(), "backend")
	assert.Contains(t, records[0].Body().AsString(), "query")
}

func TestLogObserverBothErrorAndSlow(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, 50*time.Millisecond)

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  100 * time.Millisecond,
		IsError:   true,
		Kind:      trace.SpanKindServer,
	})

	records := exporter.get()
	require.Len(t, records, 2, "should emit both error and slow log records")

	severities := map[otellog.Severity]bool{}
	for _, r := range records {
		severities[r.Severity()] = true
	}
	assert.True(t, severities[otellog.SeverityError])
	assert.True(t, severities[otellog.SeverityWarn])
}

func TestLogObserverNormalSpan(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, 1*time.Second)

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  10 * time.Millisecond,
		IsError:   false,
		Kind:      trace.SpanKindServer,
	})

	records := exporter.get()
	assert.Empty(t, records, "normal spans should not emit logs")
}

func TestLogObserverNoSlowThreshold(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, 0)

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  10 * time.Hour,
		IsError:   false,
		Kind:      trace.SpanKindServer,
	})

	records := exporter.get()
	assert.Empty(t, records, "slow detection should be disabled when threshold is 0")
}

func TestLogObserverAttributes(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, 0)

	obs.Observe(SpanInfo{
		Service:   "api",
		Operation: "POST /orders",
		Duration:  10 * time.Millisecond,
		IsError:   true,
		Kind:      trace.SpanKindServer,
	})

	records := exporter.get()
	require.Len(t, records, 1)

	attrMap := map[string]string{}
	records[0].WalkAttributes(func(kv otellog.KeyValue) bool {
		attrMap[kv.Key] = kv.Value.AsString()
		return true
	})
	assert.Equal(t, "api", attrMap["service.name"])
	assert.Equal(t, "POST /orders", attrMap["operation.name"])
}
