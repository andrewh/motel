// Tests for MetricObserver that records topology-defined and span-derived metrics.
// Uses the OTel SDK ManualReader to verify metric data points.
package synth

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"
)

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	return rm
}

func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func testMeters(mp metric.MeterProvider, services ...string) map[string]metric.Meter {
	m := make(map[string]metric.Meter, len(services))
	for _, name := range services {
		m[name] = mp.Meter("motel")
	}
	return m
}

func testRng() *rand.Rand {
	return rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing
}

func testTopology(svcName string, svcMetrics []MetricDefinition, opName string, opMetrics []MetricDefinition) *Topology {
	svc := &Service{
		Name:       svcName,
		Operations: map[string]*Operation{},
	}
	if svcMetrics != nil {
		svc.Metrics = svcMetrics
	}
	op := &Operation{
		Service: svc,
		Name:    opName,
		Ref:     svcName + "." + opName,
		Metrics: opMetrics,
	}
	svc.Operations[opName] = op
	return &Topology{
		Services: map[string]*Service{svcName: svc},
		Roots:    []*Operation{op},
	}
}

func TestMetricObserverSpanDerivedCounter(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	topo := testTopology("gateway", []MetricDefinition{
		{Name: "request.count", Type: "counter"},
	}, "GET /users", nil)

	obs, err := NewMetricObserver(testMeters(mp, "gateway"), topo, testRng())
	require.NoError(t, err)

	obs.Observe(SpanInfo{Service: "gateway", Operation: "GET /users", Duration: 50 * time.Millisecond, Kind: trace.SpanKindServer})
	obs.Observe(SpanInfo{Service: "gateway", Operation: "GET /users", Duration: 30 * time.Millisecond, Kind: trace.SpanKindServer})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "request.count")
	require.NotNil(t, m, "request.count metric should exist")

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "span-derived counter should be Sum[int64]")
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(2), sum.DataPoints[0].Value)
}

func TestMetricObserverSpanDerivedHistogram(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	topo := testTopology("backend", []MetricDefinition{
		{Name: "request.duration", Type: "histogram", Unit: "ms"},
	}, "query", nil)

	obs, err := NewMetricObserver(testMeters(mp, "backend"), topo, testRng())
	require.NoError(t, err)

	obs.Observe(SpanInfo{Service: "backend", Operation: "query", Duration: 100 * time.Millisecond, Kind: trace.SpanKindClient})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "request.duration")
	require.NotNil(t, m, "request.duration metric should exist")

	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "duration should be a Histogram[float64]")
	require.Len(t, hist.DataPoints, 1)
	assert.Equal(t, uint64(1), hist.DataPoints[0].Count)
	assert.InDelta(t, 100.0, hist.DataPoints[0].Sum, 0.1)
}

func TestMetricObserverTopologyDefinedGauge(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	dist := FloatDistribution{Mean: 0.65, StdDev: 0}
	topo := testTopology("svc", []MetricDefinition{
		{Name: "cpu.utilisation", Type: "gauge", Value: &dist},
	}, "op", nil)

	_, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	// Gauge fires on collection, not on Observe
	rm := collectMetrics(t, reader)
	m := findMetric(rm, "cpu.utilisation")
	require.NotNil(t, m, "gauge should exist after collection")

	gauge, ok := m.Data.(metricdata.Gauge[float64])
	require.True(t, ok, "should be a Gauge[float64]")
	require.Len(t, gauge.DataPoints, 1)
	assert.InDelta(t, 0.65, gauge.DataPoints[0].Value, 0.01)
}

func TestMetricObserverUpDownCounter(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	topo := testTopology("svc", []MetricDefinition{
		{Name: "active_requests", Type: "updowncounter"},
	}, "op", nil)

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	// Start 3 spans
	obs.ObserveStart("svc", "op")
	obs.ObserveStart("svc", "op")
	obs.ObserveStart("svc", "op")

	// End 1 span
	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 10 * time.Millisecond})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "active_requests")
	require.NotNil(t, m)

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(2), sum.DataPoints[0].Value, "3 starts - 1 end = 2 active")
}

func TestMetricObserverOperationScoping(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	// Service-level metric fires for all ops, operation-level only for its op
	svc := &Service{
		Name:       "svc",
		Operations: map[string]*Operation{},
		Metrics: []MetricDefinition{
			{Name: "svc.count", Type: "counter"},
		},
	}
	opA := &Operation{
		Service: svc,
		Name:    "a",
		Ref:     "svc.a",
		Metrics: []MetricDefinition{
			{Name: "a.count", Type: "counter"},
		},
	}
	opB := &Operation{
		Service: svc,
		Name:    "b",
		Ref:     "svc.b",
	}
	svc.Operations["a"] = opA
	svc.Operations["b"] = opB

	topo := &Topology{
		Services: map[string]*Service{"svc": svc},
		Roots:    []*Operation{opA, opB},
	}

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	obs.Observe(SpanInfo{Service: "svc", Operation: "a", Duration: 10 * time.Millisecond})
	obs.Observe(SpanInfo{Service: "svc", Operation: "b", Duration: 10 * time.Millisecond})

	rm := collectMetrics(t, reader)

	// svc.count should have 2 data points (one per operation)
	svcCount := findMetric(rm, "svc.count")
	require.NotNil(t, svcCount)
	sum, ok := svcCount.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	assert.Equal(t, int64(2), total, "service-level counter fires for both operations")

	// a.count should only have 1 data point for operation "a"
	aCount := findMetric(rm, "a.count")
	require.NotNil(t, aCount)
	aSum, ok := aCount.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, aSum.DataPoints, 1)
	assert.Equal(t, int64(1), aSum.DataPoints[0].Value)
}

func TestMetricObserverAttributes(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	topo := testTopology("api", nil, "POST /orders", []MetricDefinition{
		{
			Name: "order.count",
			Type: "counter",
			Attributes: map[string]AttributeGenerator{
				"region": &StaticValue{Value: "us-east"},
			},
		},
	})

	obs, err := NewMetricObserver(testMeters(mp, "api"), topo, testRng())
	require.NoError(t, err)

	obs.Observe(SpanInfo{Service: "api", Operation: "POST /orders", Duration: 25 * time.Millisecond})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "order.count")
	require.NotNil(t, m)

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)

	dp := sum.DataPoints[0]
	expected := attribute.NewSet(
		attribute.String("operation.name", "POST /orders"),
		attribute.String("region", "us-east"),
	)
	assert.True(t, dp.Attributes.Equals(&expected),
		"metric attributes should contain operation.name and region, got %v", dp.Attributes)
}

func TestMetricObserverSubMillisecondDuration(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	topo := testTopology("svc", []MetricDefinition{
		{Name: "dur", Type: "histogram", Unit: "ms"},
	}, "op", nil)

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 500 * time.Microsecond})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "dur")
	require.NotNil(t, m)

	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, hist.DataPoints, 1)
	assert.InDelta(t, 0.5, hist.DataPoints[0].Sum, 0.01,
		"500us should record as 0.5ms")
}

func TestMetricObserverNoMetricsDefined(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	topo := testTopology("svc", nil, "op", nil)

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	// Should not panic with no metrics defined
	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 10 * time.Millisecond})
	obs.ObserveStart("svc", "op")

	rm := collectMetrics(t, reader)
	assert.Empty(t, rm.ScopeMetrics)
}

func TestMetricObserverTopologyDefinedCounter(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	dist := FloatDistribution{Mean: 1024, StdDev: 0}
	topo := testTopology("svc", []MetricDefinition{
		{Name: "bytes.sent", Type: "counter", Unit: "By", Value: &dist},
	}, "op", nil)

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 10 * time.Millisecond})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "bytes.sent")
	require.NotNil(t, m)

	sum, ok := m.Data.(metricdata.Sum[float64])
	require.True(t, ok, "topology-defined counter should be Sum[float64]")
	require.Len(t, sum.DataPoints, 1)
	assert.InDelta(t, 1024.0, sum.DataPoints[0].Value, 0.1)
}

func TestMetricObserverHistogramDurationUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		unit     string
		duration time.Duration
		expected float64
	}{
		{"ms", 100 * time.Millisecond, 100.0},
		{"s", 2 * time.Second, 2.0},
		{"us", 500 * time.Microsecond, 500.0},
		{"", 100 * time.Millisecond, 100.0}, // default is ms
	}

	for _, tt := range tests {
		t.Run("unit="+tt.unit, func(t *testing.T) {
			t.Parallel()

			reader := sdkmetric.NewManualReader()
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

			topo := testTopology("svc", []MetricDefinition{
				{Name: "dur", Type: "histogram", Unit: tt.unit},
			}, "op", nil)

			obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
			require.NoError(t, err)

			obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: tt.duration})

			rm := collectMetrics(t, reader)
			m := findMetric(rm, "dur")
			require.NotNil(t, m)

			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok)
			require.Len(t, hist.DataPoints, 1)
			assert.InDelta(t, tt.expected, hist.DataPoints[0].Sum, 0.1)
		})
	}
}
