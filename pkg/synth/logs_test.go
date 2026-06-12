// Tests for LogObserver covering topology-defined log templates and the
// derived error/slow fallback logs. Uses an in-memory log exporter to
// capture and verify emitted records.
package synth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
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

func newTestLogObserver(t *testing.T, topo *Topology, slowThreshold time.Duration, services ...string) (*LogObserver, *memoryLogExporter) {
	t.Helper()
	exporter := &memoryLogExporter{}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)),
	)
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	loggers := make(map[string]otellog.Logger, len(services))
	for _, name := range services {
		loggers[name] = lp.Logger("motel")
	}
	obs, err := NewLogObserver(loggers, topo, slowThreshold, testRng())
	require.NoError(t, err)
	return obs, exporter
}

// testLogTopology builds a single-service, single-operation topology with
// the given service-level and operation-level log definitions.
func testLogTopology(svcName string, svcLogs []LogDefinition, opName string, opLogs []LogDefinition) *Topology {
	svc := &Service{
		Name:       svcName,
		Operations: map[string]*Operation{},
		Logs:       svcLogs,
	}
	op := &Operation{
		Service: svc,
		Name:    opName,
		Ref:     svcName + "." + opName,
		Logs:    opLogs,
	}
	svc.Operations[opName] = op
	return &Topology{
		Services: map[string]*Service{svcName: svc},
		Roots:    []*Operation{op},
	}
}

// alwaysLog returns a LogDefinition with sensible defaults for tests.
func alwaysLog(severity, body string) LogDefinition {
	return LogDefinition{Severity: severity, Body: body, Probability: 1.0}
}

func logAttrMap(r sdklog.Record) map[string]otellog.Value {
	attrs := map[string]otellog.Value{}
	r.WalkAttributes(func(kv otellog.KeyValue) bool {
		attrs[kv.Key] = kv.Value
		return true
	})
	return attrs
}

func TestLogObserverErrorSpan(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, nil, 0, "svc", "api")

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

	obs, exporter := newTestLogObserver(t, nil, 100*time.Millisecond, "backend")

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

	obs, exporter := newTestLogObserver(t, nil, 50*time.Millisecond, "svc")

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

	obs, exporter := newTestLogObserver(t, nil, 1*time.Second, "svc")

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

	obs, exporter := newTestLogObserver(t, nil, 0, "svc", "api")

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

func TestLogObserverTimestamp(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, nil, 10*time.Millisecond, "svc")
	past := time.Now().Add(-1 * time.Hour)

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Timestamp: past,
		Duration:  100 * time.Millisecond,
		IsError:   true,
		Kind:      trace.SpanKindServer,
	})

	records := exporter.get()
	require.Len(t, records, 2, "should emit both error and slow log records")
	for _, r := range records {
		assert.Equal(t, past, r.Timestamp(),
			"log record timestamp should match the span timestamp")
	}
}

func TestLogObserverAttributes(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, nil, 0, "svc", "api")

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
	assert.Equal(t, "POST /orders", attrMap["operation.name"])
	assert.Empty(t, attrMap["service.name"], "service.name should be on resource, not log attributes")
}

func TestLogObserverTopologyServiceLevel(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("gateway", []LogDefinition{
		alwaysLog("INFO", "request handled"),
	}, "GET /api", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "gateway")

	obs.Observe(SpanInfo{
		Service:   "gateway",
		Operation: "GET /api",
		Duration:  10 * time.Millisecond,
		Kind:      trace.SpanKindServer,
	})

	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t, otellog.SeverityInfo, records[0].Severity())
	assert.Equal(t, "INFO", records[0].SeverityText())
	assert.Equal(t, "request handled", records[0].Body().AsString())
	assert.Equal(t, "GET /api", logAttrMap(records[0])["operation.name"].AsString())
}

func TestLogObserverTopologyOperationScoping(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", nil, "create", []LogDefinition{
		alwaysLog("DEBUG", "creating record"),
	})
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.Observe(SpanInfo{Service: "svc", Operation: "create", Kind: trace.SpanKindServer})
	obs.Observe(SpanInfo{Service: "svc", Operation: "delete", Kind: trace.SpanKindServer})

	records := exporter.get()
	require.Len(t, records, 1, "operation-level log should fire only for its operation")
	assert.Equal(t, "creating record", records[0].Body().AsString())
}

func TestLogObserverTopologyConditions(t *testing.T) {
	t.Parallel()

	errLog := alwaysLog("ERROR", "request failed")
	errLog.Condition = logConditionError
	okLog := alwaysLog("INFO", "request succeeded")
	okLog.Condition = logConditionSuccess
	slowLog := alwaysLog("WARN", "request slow")
	slowLog.Condition = logConditionSlow

	topo := testLogTopology("svc", []LogDefinition{errLog, okLog, slowLog}, "op", nil)
	obs, exporter := newTestLogObserver(t, topo, 50*time.Millisecond, "svc")

	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 10 * time.Millisecond, IsError: true})
	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 100 * time.Millisecond, IsError: false})

	records := exporter.get()
	require.Len(t, records, 3)
	assert.Equal(t, "request failed", records[0].Body().AsString())
	assert.Equal(t, "request succeeded", records[1].Body().AsString())
	assert.Equal(t, "request slow", records[2].Body().AsString())
}

func TestLogObserverTopologySlowConditionDisabled(t *testing.T) {
	t.Parallel()

	slowLog := alwaysLog("WARN", "request slow")
	slowLog.Condition = logConditionSlow
	topo := testLogTopology("svc", []LogDefinition{slowLog}, "op", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 10 * time.Hour})

	assert.Empty(t, exporter.get(), "slow condition should never fire when threshold is 0")
}

func TestLogObserverTopologyProbability(t *testing.T) {
	t.Parallel()

	never := alwaysLog("INFO", "never emitted")
	never.Probability = 0
	always := alwaysLog("INFO", "always emitted")

	topo := testLogTopology("svc", []LogDefinition{never, always}, "op", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	for range 20 {
		obs.Observe(SpanInfo{Service: "svc", Operation: "op"})
	}

	records := exporter.get()
	require.Len(t, records, 20)
	for _, r := range records {
		assert.Equal(t, "always emitted", r.Body().AsString())
	}
}

func TestLogObserverTopologyTiming(t *testing.T) {
	t.Parallel()

	start := alwaysLog("INFO", "at start")
	end := alwaysLog("INFO", "at end")
	end.AtEnd = true
	delayed := alwaysLog("INFO", "delayed")
	delayed.Delay = 5 * time.Millisecond

	topo := testLogTopology("svc", []LogDefinition{start, end, delayed}, "op", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	ts := time.Now().Add(-1 * time.Hour)
	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Timestamp: ts,
		Duration:  100 * time.Millisecond,
	})

	records := exporter.get()
	require.Len(t, records, 3)
	assert.Equal(t, ts, records[0].Timestamp(), "default anchor is span start")
	assert.Equal(t, ts.Add(100*time.Millisecond), records[1].Timestamp(), "at: end anchors to span end")
	assert.Equal(t, ts.Add(5*time.Millisecond), records[2].Timestamp(), "delay offsets from the anchor")
}

func TestLogObserverTopologyBodyInterpolation(t *testing.T) {
	t.Parallel()

	gen, err := NewAttributeGenerator(AttributeValueConfig{Value: "TimeoutError"})
	require.NoError(t, err)

	def := alwaysLog("ERROR", "{error.type} in {service.name} {operation.name}: method={http.request.method} missing={no.such.key}")
	def.Attributes = NewAttributes(map[string]AttributeGenerator{"error.type": gen})

	topo := testLogTopology("svc", []LogDefinition{def}, "op", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Attrs:     []attribute.KeyValue{attribute.String("http.request.method", "GET")},
	})

	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t,
		"TimeoutError in svc op: method=GET missing={no.such.key}",
		records[0].Body().AsString())
}

func TestLogObserverTopologyTypedAttributes(t *testing.T) {
	t.Parallel()

	strGen, err := NewAttributeGenerator(AttributeValueConfig{Value: "checkout"})
	require.NoError(t, err)
	intGen, err := NewAttributeGenerator(AttributeValueConfig{Range: []int64{42, 42}})
	require.NoError(t, err)

	def := alwaysLog("INFO", "typed attributes")
	def.Attributes = NewAttributes(map[string]AttributeGenerator{
		"app.flow":    strGen,
		"app.retries": intGen,
	})

	topo := testLogTopology("svc", []LogDefinition{def}, "op", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.Observe(SpanInfo{Service: "svc", Operation: "op"})

	records := exporter.get()
	require.Len(t, records, 1)
	attrs := logAttrMap(records[0])
	assert.Equal(t, otellog.KindString, attrs["app.flow"].Kind())
	assert.Equal(t, "checkout", attrs["app.flow"].AsString())
	assert.Equal(t, otellog.KindInt64, attrs["app.retries"].Kind())
	assert.Equal(t, int64(42), attrs["app.retries"].AsInt64())
}

func TestLogObserverTopologySuppressesDerived(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", []LogDefinition{
		alwaysLog("INFO", "custom log"),
	}, "op", nil)
	obs, exporter := newTestLogObserver(t, topo, 10*time.Millisecond, "svc")

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  100 * time.Millisecond,
		IsError:   true,
	})

	records := exporter.get()
	require.Len(t, records, 1, "topology logs should replace derived error/slow logs")
	assert.Equal(t, "custom log", records[0].Body().AsString())
}

func TestLogObserverTraceCorrelation(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", []LogDefinition{
		alwaysLog("INFO", "correlated"),
	}, "op", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	traceID := trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	spanID := trace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "op", SpanContext: sc})

	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t, traceID, records[0].TraceID(), "log record should carry the span's trace ID")
	assert.Equal(t, spanID, records[0].SpanID(), "log record should carry the span's span ID")
}

func TestLogObserverDerivedTraceCorrelation(t *testing.T) {
	t.Parallel()

	obs, exporter := newTestLogObserver(t, nil, 0, "svc")

	traceID := trace.TraceID{0xaa, 0xbb, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	spanID := trace.SpanID{0xcc, 0xdd, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "op", IsError: true, SpanContext: sc})

	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t, traceID, records[0].TraceID(), "derived log should carry the span's trace ID")
	assert.Equal(t, spanID, records[0].SpanID(), "derived log should carry the span's span ID")
}

func TestLogObserverScenarioAddOperationScope(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", nil, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc.query": {AddLogs: []LogDefinition{
			{Severity: "ERROR", Body: "connection pool exhausted", Probability: 1.0},
		}},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query"})
	obs.Observe(SpanInfo{Service: "svc", Operation: "other"})

	records := exporter.get()
	require.Len(t, records, 1, "operation-scoped added log should fire only for its operation")
	assert.Equal(t, otellog.SeverityError, records[0].Severity())
	assert.Equal(t, "connection pool exhausted", records[0].Body().AsString())
}

func TestLogObserverScenarioAddServiceScope(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", []LogDefinition{
		alwaysLog("INFO", "base log"),
	}, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc": {AddLogs: []LogDefinition{
			{Severity: "WARN", Body: "degraded mode", Probability: 1.0},
		}},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query"})

	records := exporter.get()
	require.Len(t, records, 2, "base and added logs should both emit")
	assert.Equal(t, "base log", records[0].Body().AsString())
	assert.Equal(t, "degraded mode", records[1].Body().AsString())
}

func TestLogObserverScenarioAddRespectsCondition(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", nil, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc.query": {AddLogs: []LogDefinition{
			{Severity: "ERROR", Body: "incident error", Condition: logConditionError, Probability: 1.0},
		}},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query", IsError: false})
	assert.Empty(t, exporter.get(), "error-conditioned added log should not fire for success spans")

	obs.Observe(SpanInfo{Service: "svc", Operation: "query", IsError: true})
	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t, "incident error", records[0].Body().AsString())
}

func TestLogObserverScenarioDisableMutesBase(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", []LogDefinition{
		alwaysLog("INFO", "base log"),
	}, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc": {DisableLogs: true},
	})
	obs.Observe(SpanInfo{Service: "svc", Operation: "query", IsError: true})
	assert.Empty(t, exporter.get(), "disable should mute base topology logs")

	obs.SetOverrides(nil)
	obs.Observe(SpanInfo{Service: "svc", Operation: "query"})
	records := exporter.get()
	require.Len(t, records, 1, "clearing overrides should restore base logs")
	assert.Equal(t, "base log", records[0].Body().AsString())
}

func TestLogObserverScenarioDisableOperationScope(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", []LogDefinition{
		alwaysLog("INFO", "base log"),
	}, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc.query": {DisableLogs: true},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query"})
	obs.Observe(SpanInfo{Service: "svc", Operation: "other"})

	records := exporter.get()
	require.Len(t, records, 1, "operation-scoped disable should mute only that operation's spans")
	assert.Equal(t, "base log", records[0].Body().AsString())
}

func TestLogObserverScenarioDisableMutesDerived(t *testing.T) {
	t.Parallel()

	// Service with no topology logs: derived error logs normally fire.
	topo := testLogTopology("svc", nil, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc": {DisableLogs: true},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query", IsError: true})
	assert.Empty(t, exporter.get(), "disable should mute derived error logs too")
}

func TestLogObserverScenarioDisableOperationScopeMutesDerived(t *testing.T) {
	t.Parallel()

	// Service with no topology logs: derived error logs normally fire.
	topo := testLogTopology("svc", nil, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc.query": {DisableLogs: true},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query", IsError: true})
	assert.Empty(t, exporter.get(), "operation-scoped disable should mute derived logs for that operation")

	obs.Observe(SpanInfo{Service: "svc", Operation: "other", IsError: true})
	records := exporter.get()
	require.Len(t, records, 1, "derived logs should still fire for other operations")
	assert.Equal(t, otellog.SeverityError, records[0].Severity())
}

func TestLogObserverScenarioAddAndDisableReplaces(t *testing.T) {
	t.Parallel()

	topo := testLogTopology("svc", []LogDefinition{
		alwaysLog("INFO", "base log"),
	}, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc": {
			DisableLogs: true,
			AddLogs: []LogDefinition{
				{Severity: "FATAL", Body: "total outage", Probability: 1.0},
			},
		},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query"})

	records := exporter.get()
	require.Len(t, records, 1, "disable plus add should replace base logs")
	assert.Equal(t, "total outage", records[0].Body().AsString())
}

func TestLogObserverScenarioAddSuppressesDerived(t *testing.T) {
	t.Parallel()

	// Service with no topology logs: an added log counts as a topology log
	// and suppresses the derived fallback while active.
	topo := testLogTopology("svc", nil, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc.query": {AddLogs: []LogDefinition{
			{Severity: "ERROR", Body: "incident error", Condition: logConditionError, Probability: 1.0},
		}},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query", IsError: true})

	records := exporter.get()
	require.Len(t, records, 1, "added log should replace the derived error log")
	assert.Equal(t, "incident error", records[0].Body().AsString())
}

func TestLogObserverScenarioAddInterpolation(t *testing.T) {
	t.Parallel()

	gen, err := NewAttributeGenerator(AttributeValueConfig{Value: "PoolExhaustedError"})
	require.NoError(t, err)

	topo := testLogTopology("svc", nil, "query", nil)
	obs, exporter := newTestLogObserver(t, topo, 0, "svc")

	obs.SetOverrides(map[string]Override{
		"svc.query": {AddLogs: []LogDefinition{{
			Severity:    "ERROR",
			Body:        "{error.type} in {operation.name}",
			Probability: 1.0,
			Attributes:  NewAttributes(map[string]AttributeGenerator{"error.type": gen}),
		}}},
	})

	obs.Observe(SpanInfo{Service: "svc", Operation: "query"})

	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t, "PoolExhaustedError in query", records[0].Body().AsString())
	assert.Equal(t, "PoolExhaustedError", logAttrMap(records[0])["error.type"].AsString())
}

func TestLogObserverTopologyOtherServiceKeepsDerived(t *testing.T) {
	t.Parallel()

	// gateway defines topology logs; backend does not and keeps derived logs.
	topo := testLogTopology("gateway", []LogDefinition{
		alwaysLog("INFO", "custom"),
	}, "op", nil)
	backend := &Service{Name: "backend", Operations: map[string]*Operation{}}
	backend.Operations["query"] = &Operation{Service: backend, Name: "query", Ref: "backend.query"}
	topo.Services["backend"] = backend

	obs, exporter := newTestLogObserver(t, topo, 0, "gateway", "backend")

	obs.Observe(SpanInfo{Service: "backend", Operation: "query", IsError: true})

	records := exporter.get()
	require.Len(t, records, 1)
	assert.Equal(t, otellog.SeverityError, records[0].Severity())
	assert.Contains(t, records[0].Body().AsString(), "error in backend query")
}
