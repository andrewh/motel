// Tests for the time-offset metric exporter wrapper that shifts data point
// timestamps at export time.
package synth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// captureMetricExporter records the last exported ResourceMetrics.
type captureMetricExporter struct {
	exported *metricdata.ResourceMetrics
}

func (e *captureMetricExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (e *captureMetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *captureMetricExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	e.exported = rm
	return nil
}

func (e *captureMetricExporter) ForceFlush(context.Context) error { return nil }

func (e *captureMetricExporter) Shutdown(context.Context) error { return nil }

func TestNewTimeOffsetMetricExporterZeroOffset(t *testing.T) {
	t.Parallel()

	capture := &captureMetricExporter{}
	exporter := NewTimeOffsetMetricExporter(capture, 0)
	assert.Same(t, capture, exporter, "zero offset should return the exporter unchanged")
}

func TestTimeOffsetMetricExporterShiftsAllDataPointTypes(t *testing.T) {
	t.Parallel()

	offset := -2 * time.Hour
	start := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	exemplarTime := start.Add(30 * time.Second)

	rm := &metricdata.ResourceMetrics{
		ScopeMetrics: []metricdata.ScopeMetrics{{
			Metrics: []metricdata.Metrics{
				{Name: "gauge.int", Data: metricdata.Gauge[int64]{
					DataPoints: []metricdata.DataPoint[int64]{{StartTime: start, Time: end}},
				}},
				{Name: "gauge.float", Data: metricdata.Gauge[float64]{
					DataPoints: []metricdata.DataPoint[float64]{{StartTime: start, Time: end}},
				}},
				{Name: "sum.int", Data: metricdata.Sum[int64]{
					DataPoints: []metricdata.DataPoint[int64]{{
						StartTime: start,
						Time:      end,
						Exemplars: []metricdata.Exemplar[int64]{{Time: exemplarTime}},
					}},
				}},
				{Name: "sum.float", Data: metricdata.Sum[float64]{
					DataPoints: []metricdata.DataPoint[float64]{{StartTime: start, Time: end}},
				}},
				{Name: "hist.int", Data: metricdata.Histogram[int64]{
					DataPoints: []metricdata.HistogramDataPoint[int64]{{
						StartTime: start,
						Time:      end,
						Exemplars: []metricdata.Exemplar[int64]{{Time: exemplarTime}},
					}},
				}},
				{Name: "hist.float", Data: metricdata.Histogram[float64]{
					DataPoints: []metricdata.HistogramDataPoint[float64]{{StartTime: start, Time: end}},
				}},
				{Name: "exphist.int", Data: metricdata.ExponentialHistogram[int64]{
					DataPoints: []metricdata.ExponentialHistogramDataPoint[int64]{{StartTime: start, Time: end}},
				}},
				{Name: "exphist.float", Data: metricdata.ExponentialHistogram[float64]{
					DataPoints: []metricdata.ExponentialHistogramDataPoint[float64]{{
						StartTime: start,
						Time:      end,
						Exemplars: []metricdata.Exemplar[float64]{{Time: exemplarTime}},
					}},
				}},
				{Name: "summary", Data: metricdata.Summary{
					DataPoints: []metricdata.SummaryDataPoint{{StartTime: start, Time: end}},
				}},
			},
		}},
	}

	capture := &captureMetricExporter{}
	exporter := NewTimeOffsetMetricExporter(capture, offset)
	require.NoError(t, exporter.Export(context.Background(), rm))
	require.NotNil(t, capture.exported)

	wantStart := start.Add(offset)
	wantEnd := end.Add(offset)
	wantExemplar := exemplarTime.Add(offset)

	for _, m := range capture.exported.ScopeMetrics[0].Metrics {
		switch data := m.Data.(type) {
		case metricdata.Gauge[int64]:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
		case metricdata.Gauge[float64]:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
		case metricdata.Sum[int64]:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
			assert.Equal(t, wantExemplar, data.DataPoints[0].Exemplars[0].Time, m.Name)
		case metricdata.Sum[float64]:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
		case metricdata.Histogram[int64]:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
			assert.Equal(t, wantExemplar, data.DataPoints[0].Exemplars[0].Time, m.Name)
		case metricdata.Histogram[float64]:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
		case metricdata.ExponentialHistogram[int64]:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
		case metricdata.ExponentialHistogram[float64]:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
			assert.Equal(t, wantExemplar, data.DataPoints[0].Exemplars[0].Time, m.Name)
		case metricdata.Summary:
			assert.Equal(t, wantStart, data.DataPoints[0].StartTime, m.Name)
			assert.Equal(t, wantEnd, data.DataPoints[0].Time, m.Name)
		default:
			t.Fatalf("unhandled aggregation type %T for metric %s", data, m.Name)
		}
	}
}

func TestTimeOffsetMetricExporterEndToEnd(t *testing.T) {
	t.Parallel()

	offset := -24 * time.Hour
	capture := &captureMetricExporter{}
	reader := sdkmetric.NewPeriodicReader(NewTimeOffsetMetricExporter(capture, offset))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { require.NoError(t, mp.Shutdown(context.Background())) }()

	counter, err := mp.Meter("motel").Int64Counter("requests.count")
	require.NoError(t, err)

	before := time.Now()
	counter.Add(context.Background(), 1)
	require.NoError(t, reader.ForceFlush(context.Background()))
	after := time.Now()

	require.NotNil(t, capture.exported)
	m := findMetric(*capture.exported, "requests.count")
	require.NotNil(t, m)
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)

	dp := sum.DataPoints[0]
	assert.False(t, dp.Time.Before(before.Add(offset)), "data point time should be shifted by offset")
	assert.False(t, dp.Time.After(after.Add(offset)), "data point time should be shifted by offset")
	assert.False(t, dp.StartTime.After(dp.Time), "start time should not be after time")
}
