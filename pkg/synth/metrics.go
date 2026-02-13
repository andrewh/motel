// MetricObserver derives request duration, count, and error metrics from spans.
// Uses the OTel Metrics API to record measurements with service and operation attributes.
package synth

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MetricObserver records derived metrics for each observed span.
type MetricObserver struct {
	duration metric.Float64Histogram
	requests metric.Int64Counter
	errors   metric.Int64Counter
}

// NewMetricObserver creates a MetricObserver backed by the given MeterProvider.
func NewMetricObserver(mp metric.MeterProvider) (*MetricObserver, error) {
	meter := mp.Meter("motel-synth")

	duration, err := meter.Float64Histogram("synth.request.duration",
		metric.WithUnit("ms"),
		metric.WithDescription("Duration of synthetic requests in milliseconds"),
	)
	if err != nil {
		return nil, err
	}

	requests, err := meter.Int64Counter("synth.request.count",
		metric.WithDescription("Number of synthetic requests"),
	)
	if err != nil {
		return nil, err
	}

	errors, err := meter.Int64Counter("synth.error.count",
		metric.WithDescription("Number of synthetic request errors"),
	)
	if err != nil {
		return nil, err
	}

	return &MetricObserver{
		duration: duration,
		requests: requests,
		errors:   errors,
	}, nil
}

// Observe records metrics derived from the completed span.
func (m *MetricObserver) Observe(info SpanInfo) {
	attrs := metric.WithAttributes(
		attribute.String("service.name", info.Service),
		attribute.String("operation.name", info.Operation),
	)
	m.requests.Add(context.Background(), 1, attrs)
	m.duration.Record(context.Background(), float64(info.Duration)/float64(time.Millisecond), attrs)
	if info.IsError {
		m.errors.Add(context.Background(), 1, attrs)
	}
}
