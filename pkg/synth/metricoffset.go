// Metric timestamp offsetting. The OTel Metrics API does not accept
// caller-supplied timestamps — data points are timestamped at collection time
// by the SDK reader. Rewriting timestamps at export time is the only way to
// backdate metric data points without forking the SDK.
package synth

import (
	"context"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// timeOffsetMetricExporter wraps a metric exporter and shifts StartTime, Time,
// and exemplar timestamps on every exported data point by a fixed offset.
type timeOffsetMetricExporter struct {
	sdkmetric.Exporter
	offset time.Duration
}

// NewTimeOffsetMetricExporter wraps exporter so that all exported metric data
// point timestamps are shifted by offset. A zero offset returns the exporter
// unchanged.
func NewTimeOffsetMetricExporter(exporter sdkmetric.Exporter, offset time.Duration) sdkmetric.Exporter {
	if offset == 0 {
		return exporter
	}
	return &timeOffsetMetricExporter{Exporter: exporter, offset: offset}
}

func (e *timeOffsetMetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	shiftResourceMetrics(rm, e.offset)
	return e.Exporter.Export(ctx, rm)
}

func shiftResourceMetrics(rm *metricdata.ResourceMetrics, offset time.Duration) {
	for i := range rm.ScopeMetrics {
		metrics := rm.ScopeMetrics[i].Metrics
		for j := range metrics {
			shiftAggregation(metrics[j].Data, offset)
		}
	}
}

// shiftAggregation mutates the data points of agg in place. The metricdata
// aggregation types hold slices, so mutating elements through a copy of the
// interface value updates the shared backing arrays.
func shiftAggregation(agg metricdata.Aggregation, offset time.Duration) {
	switch a := agg.(type) {
	case metricdata.Gauge[int64]:
		shiftDataPoints(a.DataPoints, offset)
	case metricdata.Gauge[float64]:
		shiftDataPoints(a.DataPoints, offset)
	case metricdata.Sum[int64]:
		shiftDataPoints(a.DataPoints, offset)
	case metricdata.Sum[float64]:
		shiftDataPoints(a.DataPoints, offset)
	case metricdata.Histogram[int64]:
		shiftHistogramDataPoints(a.DataPoints, offset)
	case metricdata.Histogram[float64]:
		shiftHistogramDataPoints(a.DataPoints, offset)
	case metricdata.ExponentialHistogram[int64]:
		shiftExponentialHistogramDataPoints(a.DataPoints, offset)
	case metricdata.ExponentialHistogram[float64]:
		shiftExponentialHistogramDataPoints(a.DataPoints, offset)
	case metricdata.Summary:
		shiftSummaryDataPoints(a.DataPoints, offset)
	}
}

func shiftDataPoints[N int64 | float64](dps []metricdata.DataPoint[N], offset time.Duration) {
	for i := range dps {
		dps[i].StartTime = dps[i].StartTime.Add(offset)
		dps[i].Time = dps[i].Time.Add(offset)
		shiftExemplars(dps[i].Exemplars, offset)
	}
}

func shiftHistogramDataPoints[N int64 | float64](dps []metricdata.HistogramDataPoint[N], offset time.Duration) {
	for i := range dps {
		dps[i].StartTime = dps[i].StartTime.Add(offset)
		dps[i].Time = dps[i].Time.Add(offset)
		shiftExemplars(dps[i].Exemplars, offset)
	}
}

func shiftExponentialHistogramDataPoints[N int64 | float64](dps []metricdata.ExponentialHistogramDataPoint[N], offset time.Duration) {
	for i := range dps {
		dps[i].StartTime = dps[i].StartTime.Add(offset)
		dps[i].Time = dps[i].Time.Add(offset)
		shiftExemplars(dps[i].Exemplars, offset)
	}
}

func shiftSummaryDataPoints(dps []metricdata.SummaryDataPoint, offset time.Duration) {
	for i := range dps {
		dps[i].StartTime = dps[i].StartTime.Add(offset)
		dps[i].Time = dps[i].Time.Add(offset)
	}
}

func shiftExemplars[N int64 | float64](es []metricdata.Exemplar[N], offset time.Duration) {
	for i := range es {
		es[i].Time = es[i].Time.Add(offset)
	}
}
