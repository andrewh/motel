// MetricObserver records topology-defined and span-derived metrics.
// Uses the OTel Metrics API to record measurements with service and operation attributes.
package synth

import (
	"context"
	"math/rand/v2"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metricInstrument holds one OTel instrument and its recording configuration.
type metricInstrument struct {
	// Exactly one of these is non-nil.
	int64Counter         metric.Int64Counter
	int64UpDownCounter   metric.Int64UpDownCounter
	float64Counter       metric.Float64Counter
	float64Histogram     metric.Float64Histogram
	float64UpDownCounter metric.Float64UpDownCounter

	value     *FloatDistribution // nil = span-derived
	unit      string
	attrGens  map[string]AttributeGenerator
	operation string // non-empty if operation-level (fires only for this op)
}

// MetricObserver records derived metrics for each observed span.
type MetricObserver struct {
	services map[string][]metricInstrument
	rng      *rand.Rand
}

// NewMetricObserver creates a MetricObserver from topology metric definitions.
// Each meter should carry a resource with the correct service.name for its service.
func NewMetricObserver(meters map[string]metric.Meter, topo *Topology, rng *rand.Rand) (*MetricObserver, error) {
	services := make(map[string][]metricInstrument)

	for svcName, svc := range topo.Services {
		meter := meters[svcName]
		if meter == nil {
			continue
		}

		var instruments []metricInstrument

		// Service-level metrics (fire for every span in this service)
		for _, md := range svc.Metrics {
			inst, err := createInstrument(meter, md, "")
			if err != nil {
				return nil, err
			}
			instruments = append(instruments, inst)
		}

		// Operation-level metrics (fire only for the specific operation)
		for _, op := range svc.Operations {
			for _, md := range op.Metrics {
				inst, err := createInstrument(meter, md, op.Name)
				if err != nil {
					return nil, err
				}
				instruments = append(instruments, inst)
			}
		}

		if len(instruments) > 0 {
			services[svcName] = instruments
		}
	}

	return &MetricObserver{services: services, rng: rng}, nil
}

// createInstrument builds a metricInstrument from a MetricDefinition.
func createInstrument(meter metric.Meter, md MetricDefinition, operation string) (metricInstrument, error) {
	inst := metricInstrument{
		value:     md.Value,
		unit:      md.Unit,
		attrGens:  md.Attributes,
		operation: operation,
	}

	switch md.Type {
	case "counter":
		if md.Value != nil {
			// Topology-defined: sampled float value per recording
			var copts []metric.Float64CounterOption
			if md.Unit != "" {
				copts = append(copts, metric.WithUnit(md.Unit))
			}
			c, err := meter.Float64Counter(md.Name, copts...)
			if err != nil {
				return metricInstrument{}, err
			}
			inst.float64Counter = c
		} else {
			// Span-derived: +1 per span
			var copts []metric.Int64CounterOption
			if md.Unit != "" {
				copts = append(copts, metric.WithUnit(md.Unit))
			}
			c, err := meter.Int64Counter(md.Name, copts...)
			if err != nil {
				return metricInstrument{}, err
			}
			inst.int64Counter = c
		}

	case "updowncounter":
		if md.Value != nil {
			var copts []metric.Float64UpDownCounterOption
			if md.Unit != "" {
				copts = append(copts, metric.WithUnit(md.Unit))
			}
			c, err := meter.Float64UpDownCounter(md.Name, copts...)
			if err != nil {
				return metricInstrument{}, err
			}
			inst.float64UpDownCounter = c
		} else {
			// Span-derived: +1 on start, -1 on end
			var copts []metric.Int64UpDownCounterOption
			if md.Unit != "" {
				copts = append(copts, metric.WithUnit(md.Unit))
			}
			c, err := meter.Int64UpDownCounter(md.Name, copts...)
			if err != nil {
				return metricInstrument{}, err
			}
			inst.int64UpDownCounter = c
		}

	case "histogram":
		var hopts []metric.Float64HistogramOption
		if md.Unit != "" {
			hopts = append(hopts, metric.WithUnit(md.Unit))
		}
		h, err := meter.Float64Histogram(md.Name, hopts...)
		if err != nil {
			return metricInstrument{}, err
		}
		inst.float64Histogram = h

	case "gauge":
		// Gauges are always topology-defined (validated earlier).
		// Register an observable gauge with a callback that samples the distribution.
		var gopts []metric.Float64ObservableGaugeOption
		if md.Unit != "" {
			gopts = append(gopts, metric.WithUnit(md.Unit))
		}
		// The gauge callback is registered via WithFloat64Callback on the gauge itself.
		// We don't store it in the instrument — it fires on collection.
		dist := md.Value
		gaugeAttrs := md.Attributes
		gaugeOp := operation
		gopts = append(gopts, metric.WithFloat64Callback(func(_ context.Context, obs metric.Float64Observer) error {
			// Create a fresh rng inside the callback — it runs asynchronously
			// during collection and cannot share the observer's rng.
			rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // synthetic data
			attrs := buildMetricAttrs(gaugeAttrs, gaugeOp, rng)
			obs.Observe(dist.Sample(rng), attrs)
			return nil
		}))
		_, err := meter.Float64ObservableGauge(md.Name, gopts...)
		if err != nil {
			return metricInstrument{}, err
		}
		// No instrument stored — the callback handles everything.
		return inst, nil
	}

	return inst, nil
}

// buildMetricAttrs constructs metric attributes from generators and adds operation.name.
func buildMetricAttrs(attrGens map[string]AttributeGenerator, operation string, rng *rand.Rand) metric.MeasurementOption {
	attrs := make([]attribute.KeyValue, 0, len(attrGens)+1)
	if operation != "" {
		attrs = append(attrs, attribute.String("operation.name", operation))
	}
	for k, gen := range attrGens {
		attrs = append(attrs, typedAttribute(k, gen.Generate(rng)))
	}
	return metric.WithAttributes(attrs...)
}

// Observe records metrics derived from the completed span.
// Note: metric data points are timestamped at collection time by the OTel SDK's
// PeriodicReader. The Metrics API does not support caller-supplied timestamps,
// so Engine.TimeOffset has no effect on metric timestamps. See issue 99.
func (m *MetricObserver) Observe(info SpanInfo) {
	instruments := m.services[info.Service]
	if len(instruments) == 0 {
		return
	}

	for i := range instruments {
		inst := &instruments[i]

		// Operation-level metrics only fire for their specific operation.
		if inst.operation != "" && inst.operation != info.Operation {
			continue
		}

		attrs := buildMetricAttrs(inst.attrGens, info.Operation, m.rng)

		switch {
		case inst.int64Counter != nil:
			inst.int64Counter.Add(context.Background(), 1, attrs)

		case inst.float64Counter != nil:
			inst.float64Counter.Add(context.Background(), inst.value.Sample(m.rng), attrs)

		case inst.int64UpDownCounter != nil:
			// -1 on span end (the +1 happens in ObserveStart)
			inst.int64UpDownCounter.Add(context.Background(), -1, attrs)

		case inst.float64UpDownCounter != nil:
			inst.float64UpDownCounter.Add(context.Background(), inst.value.Sample(m.rng), attrs)

		case inst.float64Histogram != nil:
			if inst.value != nil {
				inst.float64Histogram.Record(context.Background(), inst.value.Sample(m.rng), attrs)
			} else {
				// Span-derived: record span duration in the configured unit
				duration := durationInUnit(info.Duration, inst.unit)
				inst.float64Histogram.Record(context.Background(), duration, attrs)
			}
		}
	}
}

// ObserveStart records the start of a span for updowncounter tracking.
func (m *MetricObserver) ObserveStart(service, operation string) {
	instruments := m.services[service]
	if len(instruments) == 0 {
		return
	}

	for i := range instruments {
		inst := &instruments[i]
		if inst.int64UpDownCounter == nil {
			continue
		}
		if inst.operation != "" && inst.operation != operation {
			continue
		}

		attrs := buildMetricAttrs(inst.attrGens, operation, m.rng)
		inst.int64UpDownCounter.Add(context.Background(), 1, attrs)
	}
}

// durationInUnit converts a duration to a float64 in the given unit.
// Defaults to milliseconds if unit is empty or unrecognised.
func durationInUnit(d time.Duration, unit string) float64 {
	switch unit {
	case "s":
		return d.Seconds()
	case "us":
		return float64(d.Microseconds())
	case "ns":
		return float64(d.Nanoseconds())
	default:
		return float64(d) / float64(time.Millisecond)
	}
}
