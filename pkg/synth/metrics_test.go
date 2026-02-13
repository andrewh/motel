// Tests for MetricObserver that derives request duration, count, and error metrics.
// Uses the OTel SDK ManualReader to verify metric data points.
package synth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
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

func TestMetricObserverRequestCount(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	obs, err := NewMetricObserver(mp)
	require.NoError(t, err)

	obs.Observe(SpanInfo{
		Service:   "gateway",
		Operation: "GET /users",
		Duration:  50 * time.Millisecond,
		Kind:      trace.SpanKindServer,
	})
	obs.Observe(SpanInfo{
		Service:   "gateway",
		Operation: "GET /users",
		Duration:  30 * time.Millisecond,
		Kind:      trace.SpanKindServer,
	})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "synth.request.count")
	require.NotNil(t, m, "synth.request.count metric should exist")

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "request count should be a Sum[int64]")
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(2), sum.DataPoints[0].Value)
}

func TestMetricObserverDuration(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	obs, err := NewMetricObserver(mp)
	require.NoError(t, err)

	obs.Observe(SpanInfo{
		Service:   "backend",
		Operation: "query",
		Duration:  100 * time.Millisecond,
		Kind:      trace.SpanKindClient,
	})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "synth.request.duration")
	require.NotNil(t, m, "synth.request.duration metric should exist")

	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "duration should be a Histogram[float64]")
	require.Len(t, hist.DataPoints, 1)
	assert.Equal(t, uint64(1), hist.DataPoints[0].Count)
	assert.InDelta(t, 100.0, hist.DataPoints[0].Sum, 0.1)
}

func TestMetricObserverErrorCount(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	obs, err := NewMetricObserver(mp)
	require.NoError(t, err)

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  10 * time.Millisecond,
		IsError:   true,
		Kind:      trace.SpanKindServer,
	})
	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  10 * time.Millisecond,
		IsError:   false,
		Kind:      trace.SpanKindServer,
	})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "synth.error.count")
	require.NotNil(t, m, "synth.error.count metric should exist")

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "error count should be a Sum[int64]")
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(1), sum.DataPoints[0].Value)
}

func TestMetricObserverAttributes(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	obs, err := NewMetricObserver(mp)
	require.NoError(t, err)

	obs.Observe(SpanInfo{
		Service:   "api",
		Operation: "POST /orders",
		Duration:  25 * time.Millisecond,
		Kind:      trace.SpanKindServer,
	})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "synth.request.count")
	require.NotNil(t, m)

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)

	dp := sum.DataPoints[0]
	expected := attribute.NewSet(
		attribute.String("service.name", "api"),
		attribute.String("operation.name", "POST /orders"),
	)
	assert.True(t, dp.Attributes.Equals(&expected),
		"metric attributes should contain service.name and operation.name")
}

func TestMetricObserverSubMillisecondDuration(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	obs, err := NewMetricObserver(mp)
	require.NoError(t, err)

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  500 * time.Microsecond,
		Kind:      trace.SpanKindServer,
	})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "synth.request.duration")
	require.NotNil(t, m)

	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, hist.DataPoints, 1)
	assert.InDelta(t, 0.5, hist.DataPoints[0].Sum, 0.01,
		"500us should record as 0.5ms, not 0")
}

func TestMetricObserverNoErrorsWhenNoErrors(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	obs, err := NewMetricObserver(mp)
	require.NoError(t, err)

	obs.Observe(SpanInfo{
		Service:   "svc",
		Operation: "op",
		Duration:  10 * time.Millisecond,
		IsError:   false,
		Kind:      trace.SpanKindServer,
	})

	rm := collectMetrics(t, reader)
	m := findMetric(rm, "synth.error.count")
	if m != nil {
		sum, ok := m.Data.(metricdata.Sum[int64])
		if ok && len(sum.DataPoints) > 0 {
			assert.Equal(t, int64(0), sum.DataPoints[0].Value)
		}
	}
}
