// Tests for MetricObserver that records topology-defined and span-derived metrics.
// Uses the OTel SDK ManualReader to verify metric data points.
package synth

import (
	"context"
	"math/rand/v2"
	"os"
	"sync"
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

func TestMetricObserverConcurrentAccess(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	topo := testTopology("svc", []MetricDefinition{
		{Name: "active", Type: "updowncounter"},
		{Name: "count", Type: "counter"},
	}, "op", nil)

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	const goroutines = 10
	const iters = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iters {
				obs.ObserveStart("svc", "op")
			}
		}()
		go func() {
			defer wg.Done()
			for range iters {
				obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: time.Millisecond})
			}
		}()
	}
	wg.Wait()
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

func TestMetricObserverErrorsOnly(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	topo := testTopology("svc", []MetricDefinition{
		{Name: "error.count", Type: "counter", ErrorsOnly: true},
	}, "op", nil)

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 10 * time.Millisecond, IsError: false})
	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 10 * time.Millisecond, IsError: true})
	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: 10 * time.Millisecond, IsError: false})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "error.count")
	require.NotNil(t, m)

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(1), sum.DataPoints[0].Value, "only error spans should increment the counter")
}

func TestMetricObserverScenarioOverrideCounter(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	dist := FloatDistribution{Mean: 10, StdDev: 0}
	topo := testTopology("svc", []MetricDefinition{
		{Name: "bytes.sent", Type: "counter", Value: &dist},
	}, "op", nil)

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: time.Millisecond})

	// Activate a service-scope override
	obs.SetOverrides(map[string]Override{
		"svc": {Metrics: map[string]FloatDistribution{"bytes.sent": {Mean: 100, StdDev: 0}}},
	})
	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: time.Millisecond})

	// Clear overrides — back to the base distribution
	obs.SetOverrides(nil)
	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: time.Millisecond})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "bytes.sent")
	require.NotNil(t, m)

	sum, ok := m.Data.(metricdata.Sum[float64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	assert.InDelta(t, 120.0, sum.DataPoints[0].Value, 0.1, "10 base + 100 overridden + 10 base")
}

func TestMetricObserverScenarioOverrideGauge(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	dist := FloatDistribution{Mean: 0.4, StdDev: 0}
	topo := testTopology("svc", nil, "op", []MetricDefinition{
		{Name: "cache.hit_ratio", Type: "gauge", Value: &dist},
	})

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "cache.hit_ratio")
	require.NotNil(t, m)
	gauge := m.Data.(metricdata.Gauge[float64])
	assert.InDelta(t, 0.4, gauge.DataPoints[0].Value, 0.001)

	// Operation-scope override changes the value observed at the next collection
	obs.SetOverrides(map[string]Override{
		"svc.op": {Metrics: map[string]FloatDistribution{"cache.hit_ratio": {Mean: 0.05, StdDev: 0}}},
	})

	rm = collectMetrics(t, reader)
	m = findMetric(rm, "cache.hit_ratio")
	require.NotNil(t, m)
	gauge = m.Data.(metricdata.Gauge[float64])
	assert.InDelta(t, 0.05, gauge.DataPoints[0].Value, 0.001)
}

func TestMetricObserverOverrideOtherScopeIgnored(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	dist := FloatDistribution{Mean: 10, StdDev: 0}
	topo := testTopology("svc", []MetricDefinition{
		{Name: "bytes.sent", Type: "counter", Value: &dist},
	}, "op", nil)

	obs, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	// Override targets a different scope (operation, not service) — no effect
	obs.SetOverrides(map[string]Override{
		"svc.op": {Metrics: map[string]FloatDistribution{"bytes.sent": {Mean: 100, StdDev: 0}}},
	})
	obs.Observe(SpanInfo{Service: "svc", Operation: "op", Duration: time.Millisecond})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "bytes.sent")
	require.NotNil(t, m)
	sum := m.Data.(metricdata.Sum[float64])
	assert.InDelta(t, 10.0, sum.DataPoints[0].Value, 0.1, "service-scope metric ignores operation-scope override")
}

// TestMetricObserverEndToEnd verifies the full path: YAML → LoadConfig → BuildTopology →
// NewMetricObserver → Observe → collect. Tests all four instrument types.
func TestMetricObserverEndToEnd(t *testing.T) {
	t.Parallel()

	const yamlContent = `
version: 1
services:
  gateway:
    metrics:
      - name: http.server.request.duration
        type: histogram
        unit: ms
      - name: http.server.active_requests
        type: updowncounter
      - name: gateway.cpu.utilisation
        type: gauge
        value: "0.5"
      - name: backend.response.bytes
        type: counter
        unit: By
        value: "1024"
    operations:
      GET /api:
        duration: 40ms
        error_rate: 0%
traffic:
  rate: 1/s
`

	f := t.TempDir() + "/topology.yaml"
	require.NoError(t, os.WriteFile(f, []byte(yamlContent), 0o600))

	cfg, err := LoadConfig(f)
	require.NoError(t, err)
	require.NoError(t, ValidateConfig(cfg))

	topo, err := BuildTopology(cfg)
	require.NoError(t, err)

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	obs, err := NewMetricObserver(testMeters(mp, "gateway"), topo, testRng())
	require.NoError(t, err)

	obs.ObserveStart("gateway", "GET /api")
	obs.Observe(SpanInfo{
		Service:   "gateway",
		Operation: "GET /api",
		Duration:  40 * time.Millisecond,
		IsError:   false,
		Kind:      trace.SpanKindServer,
	})

	rm := collectMetrics(t, reader)

	hist := findMetric(rm, "http.server.request.duration")
	require.NotNil(t, hist, "histogram must be present")
	hd, ok := hist.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, hd.DataPoints, 1)
	assert.InDelta(t, 40.0, hd.DataPoints[0].Sum, 1.0)

	udc := findMetric(rm, "http.server.active_requests")
	require.NotNil(t, udc, "updowncounter must be present")
	udSum, ok := udc.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, udSum.DataPoints, 1)
	assert.Equal(t, int64(0), udSum.DataPoints[0].Value, "start +1 and end -1 should cancel")

	gauge := findMetric(rm, "gateway.cpu.utilisation")
	require.NotNil(t, gauge, "gauge must be present")
	_, ok = gauge.Data.(metricdata.Gauge[float64])
	require.True(t, ok)

	cnt := findMetric(rm, "backend.response.bytes")
	require.NotNil(t, cnt, "float64 counter must be present")
	cntSum, ok := cnt.Data.(metricdata.Sum[float64])
	require.True(t, ok)
	require.Len(t, cntSum.DataPoints, 1)
	assert.Greater(t, cntSum.DataPoints[0].Value, 0.0)
}

func TestMetricObserverGaugeWalkCorrelation(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	// With a very long mean-reversion timescale and back-to-back collections,
	// the decay factor is ~1 and the noise term ~0, so consecutive samples
	// must be nearly identical — unlike independent sampling, where draws
	// from N(0.5, 0.2) differ.
	dist := FloatDistribution{Mean: 0.5, StdDev: 0.2}
	topo := testTopology("svc", []MetricDefinition{
		{Name: "cpu", Type: "gauge", Value: &dist, Walk: time.Hour},
	}, "op", nil)

	_, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	readGauge := func() float64 {
		rm := collectMetrics(t, reader)
		m := findMetric(rm, "cpu")
		require.NotNil(t, m)
		gauge, ok := m.Data.(metricdata.Gauge[float64])
		require.True(t, ok)
		require.Len(t, gauge.DataPoints, 1)
		return gauge.DataPoints[0].Value
	}

	first := readGauge()
	second := readGauge()
	assert.InDelta(t, first, second, 0.01, "walk samples collected back-to-back should be strongly correlated")
}

func TestMetricObserverGaugeWalkMeanReversion(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	// Zero stddev: the walk has no noise, so every sample equals the mean.
	dist := FloatDistribution{Mean: 42, StdDev: 0}
	topo := testTopology("svc", []MetricDefinition{
		{Name: "depth", Type: "gauge", Value: &dist, Walk: time.Second},
	}, "op", nil)

	_, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	for range 3 {
		rm := collectMetrics(t, reader)
		m := findMetric(rm, "depth")
		require.NotNil(t, m)
		gauge := m.Data.(metricdata.Gauge[float64])
		assert.InDelta(t, 42.0, gauge.DataPoints[0].Value, 0.001)
	}
}

func TestMetricObserverGaugeBounds(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	lower, upper := 0.0, 1.0
	// Mean near the upper bound with huge variance: unclamped samples
	// frequently land outside [0, 1].
	dist := FloatDistribution{Mean: 0.9, StdDev: 10}
	topo := testTopology("svc", []MetricDefinition{
		{Name: "util", Type: "gauge", Value: &dist, Min: &lower, Max: &upper},
	}, "op", nil)

	_, err := NewMetricObserver(testMeters(mp, "svc"), topo, testRng())
	require.NoError(t, err)

	for range 10 {
		rm := collectMetrics(t, reader)
		m := findMetric(rm, "util")
		require.NotNil(t, m)
		gauge := m.Data.(metricdata.Gauge[float64])
		v := gauge.DataPoints[0].Value
		assert.GreaterOrEqual(t, v, lower)
		assert.LessOrEqual(t, v, upper)
	}
}
