// MetricObserver derives request duration, count, and error metrics from spans.
// Uses the OTel Metrics API to record measurements with service and operation attributes.
package synth

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// serviceInstruments holds the metric instruments for a single service.
type serviceInstruments struct {
	duration metric.Float64Histogram
	requests metric.Int64Counter
	errors   metric.Int64Counter
}

// MetricObserver records derived metrics for each observed span.
type MetricObserver struct {
	services map[string]*serviceInstruments
}

// NewMetricObserver creates a MetricObserver with per-service instruments.
// Each meter should carry a resource with the correct service.name for its service.
func NewMetricObserver(meters map[string]metric.Meter) (*MetricObserver, error) {
	services := make(map[string]*serviceInstruments, len(meters))
	for name, meter := range meters {
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

		services[name] = &serviceInstruments{
			duration: duration,
			requests: requests,
			errors:   errors,
		}
	}

	return &MetricObserver{services: services}, nil
}

// Observe records metrics derived from the completed span.
// Note: metric data points are timestamped at collection time by the OTel SDK's
// PeriodicReader. The Metrics API does not support caller-supplied timestamps,
// so Engine.TimeOffset has no effect on metric timestamps. See issue 99.
func (m *MetricObserver) Observe(info SpanInfo) {
	svc := m.services[info.Service]
	if svc == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("operation.name", info.Operation),
	)
	svc.requests.Add(context.Background(), 1, attrs)
	svc.duration.Record(context.Background(), float64(info.Duration)/float64(time.Millisecond), attrs)
	if info.IsError {
		svc.errors.Add(context.Background(), 1, attrs)
	}
}
