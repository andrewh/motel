// MetricObserver records topology-defined and span-derived metrics.
// Uses the OTel Metrics API to record measurements with service and operation attributes.
package synth

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
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

	name       string
	scopeRef   string             // service name or "service.operation" — key into scenario overrides
	value      *FloatDistribution // nil = span-derived
	unit       string
	attrGens   map[string]AttributeGenerator
	operation  string        // non-empty if operation-level (fires only for this op)
	errorsOnly bool          // if true, counter only increments for error spans
	interval   time.Duration // non-zero = emit on a wall-clock timer instead of per span
}

// MetricObserver records derived metrics for each observed span.
type MetricObserver struct {
	services  map[string][]metricInstrument
	intervals []metricInstrument // emitted on their own timers, not per span
	rng       *rand.Rand
	mu        sync.Mutex

	overrideMu sync.RWMutex
	overrides  map[string]Override // active scenario overrides, set by the engine
}

// NewMetricObserver creates a MetricObserver from topology metric definitions.
// Each meter should carry a resource with the correct service.name for its service.
func NewMetricObserver(meters map[string]metric.Meter, topo *Topology, rng *rand.Rand) (*MetricObserver, error) {
	m := &MetricObserver{
		services: make(map[string][]metricInstrument),
		rng:      rng,
	}

	for svcName, svc := range topo.Services {
		meter := meters[svcName]
		if meter == nil {
			continue
		}

		var instruments []metricInstrument

		// Service-level metrics (fire for every span in this service)
		for _, md := range svc.Metrics {
			inst, keep, err := m.createInstrument(meter, md, svcName, "")
			if err != nil {
				return nil, err
			}
			switch {
			case keep && inst.interval > 0:
				m.intervals = append(m.intervals, inst)
			case keep:
				instruments = append(instruments, inst)
			}
		}

		// Operation-level metrics (fire only for the specific operation)
		for _, op := range svc.Operations {
			for _, md := range op.Metrics {
				inst, keep, err := m.createInstrument(meter, md, svcName, op.Name)
				if err != nil {
					return nil, err
				}
				switch {
				case keep && inst.interval > 0:
					m.intervals = append(m.intervals, inst)
				case keep:
					instruments = append(instruments, inst)
				}
			}
		}

		if len(instruments) > 0 {
			m.services[svcName] = instruments
		}
	}

	return m, nil
}

// SetOverrides replaces the active scenario overrides. The engine calls this
// as scenario windows open and close; a nil map clears all overrides.
func (m *MetricObserver) SetOverrides(overrides map[string]Override) {
	m.overrideMu.Lock()
	m.overrides = overrides
	m.overrideMu.Unlock()
}

// effectiveValue returns the value distribution for a metric, applying any
// active scenario override for its scope. Returns false for span-derived
// metrics, which have no value distribution. Returned by value to avoid a
// heap allocation on the per-span hot path.
func (m *MetricObserver) effectiveValue(scopeRef, name string, base *FloatDistribution) (FloatDistribution, bool) {
	if base == nil {
		return FloatDistribution{}, false
	}
	m.overrideMu.RLock()
	defer m.overrideMu.RUnlock()
	if ov, ok := m.overrides[scopeRef]; ok {
		if dist, ok := ov.Metrics[name]; ok {
			return dist, true
		}
	}
	return *base, true
}

// createInstrument builds a metricInstrument from a MetricDefinition.
// Returns (inst, true, nil) for instruments that should be appended to the observer's list.
// Returns (inst, false, nil) for gauge types — the callback handles everything; no instrument entry is needed.
func (m *MetricObserver) createInstrument(meter metric.Meter, md MetricDefinition, service, operation string) (metricInstrument, bool, error) {
	scopeRef := service
	if operation != "" {
		scopeRef = service + "." + operation
	}
	inst := metricInstrument{
		name:       md.Name,
		scopeRef:   scopeRef,
		value:      md.Value,
		unit:       md.Unit,
		attrGens:   md.Attributes,
		operation:  operation,
		errorsOnly: md.ErrorsOnly,
		interval:   md.Interval,
	}

	switch md.Type {
	case metricTypeCounter:
		if md.Value != nil {
			// Topology-defined: sampled float value per recording
			var copts []metric.Float64CounterOption
			if md.Unit != "" {
				copts = append(copts, metric.WithUnit(md.Unit))
			}
			c, err := meter.Float64Counter(md.Name, copts...)
			if err != nil {
				return metricInstrument{}, false, err
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
				return metricInstrument{}, false, err
			}
			inst.int64Counter = c
		}

	case metricTypeUpDownCounter:
		if md.Value != nil {
			var copts []metric.Float64UpDownCounterOption
			if md.Unit != "" {
				copts = append(copts, metric.WithUnit(md.Unit))
			}
			c, err := meter.Float64UpDownCounter(md.Name, copts...)
			if err != nil {
				return metricInstrument{}, false, err
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
				return metricInstrument{}, false, err
			}
			inst.int64UpDownCounter = c
		}

	case metricTypeHistogram:
		var hopts []metric.Float64HistogramOption
		if md.Unit != "" {
			hopts = append(hopts, metric.WithUnit(md.Unit))
		}
		h, err := meter.Float64Histogram(md.Name, hopts...)
		if err != nil {
			return metricInstrument{}, false, err
		}
		inst.float64Histogram = h

	case metricTypeGauge:
		if md.Value == nil {
			return metricInstrument{}, false, fmt.Errorf("gauge metric %q has no value; gauges require an explicit value distribution", md.Name)
		}
		// Register an observable gauge with a callback that samples the distribution.
		var gopts []metric.Float64ObservableGaugeOption
		if md.Unit != "" {
			gopts = append(gopts, metric.WithUnit(md.Unit))
		}
		// The gauge callback fires on collection, not per span.
		// No instrument entry is needed.
		gopts = append(gopts, metric.WithFloat64Callback(m.gaugeCallback(md, scopeRef, operation)))
		_, err := meter.Float64ObservableGauge(md.Name, gopts...)
		if err != nil {
			return metricInstrument{}, false, err
		}
		return inst, false, nil
	}

	return inst, true, nil
}

// Start launches background emitters for interval-driven metrics: one
// goroutine per instrument with an interval field, each recording a sampled
// value on its own wall-clock ticker, independent of trace rate. The returned
// stop function halts the emitters and waits for them to exit. Call it before
// shutting down the meter providers. Returns a no-op when no interval metrics
// are defined.
func (m *MetricObserver) Start() (stop func()) {
	if len(m.intervals) == 0 {
		return func() {}
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := range m.intervals {
		inst := &m.intervals[i]
		wg.Go(func() {
			ticker := time.NewTicker(inst.interval)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					m.recordInterval(inst)
				}
			}
		})
	}

	return func() {
		close(done)
		wg.Wait()
	}
}

// recordInterval records one sampled measurement for an interval-driven instrument.
func (m *MetricObserver) recordInterval(inst *metricInstrument) {
	m.mu.Lock()
	attrs := buildMetricAttrs(inst.attrGens, inst.operation, m.rng)
	dist, _ := m.effectiveValue(inst.scopeRef, inst.name, inst.value)
	sampledValue := dist.Sample(m.rng)
	m.mu.Unlock()

	switch {
	case inst.float64Counter != nil:
		inst.float64Counter.Add(context.Background(), max(0, sampledValue), attrs)
	case inst.float64UpDownCounter != nil:
		inst.float64UpDownCounter.Add(context.Background(), sampledValue, attrs)
	case inst.float64Histogram != nil:
		inst.float64Histogram.Record(context.Background(), sampledValue, attrs)
	}
}

// gaugeCallback returns the observation callback for a topology-defined gauge.
// Without walk, each collection samples independently from the value
// distribution (white noise). With walk, samples follow an Ornstein-Uhlenbeck
// process: a mean-reverting random walk whose stationary distribution matches
// the configured mean and standard deviation, with mean-reversion timescale
// md.Walk. Walk timescales relate to wall-clock time between collections.
func (m *MetricObserver) gaugeCallback(md MetricDefinition, scopeRef, operation string) metric.Float64Callback {
	base := md.Value
	// Walk state persists across collection cycles in this closure.
	var walkMu sync.Mutex
	var current float64
	var lastSample time.Time

	return func(_ context.Context, obs metric.Float64Observer) error {
		// Create a fresh rng inside the callback — it runs asynchronously
		// during collection and cannot share the observer's rng.
		rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // synthetic data
		attrs := buildMetricAttrs(md.Attributes, operation, rng)
		dist, _ := m.effectiveValue(scopeRef, md.Name, base)

		if md.Walk <= 0 {
			obs.Observe(clampValue(dist.Sample(rng), md.Min, md.Max), attrs)
			return nil
		}

		walkMu.Lock()
		now := time.Now()
		if lastSample.IsZero() {
			current = dist.Sample(rng)
		} else {
			// Exact OU discretisation: decay toward the mean over the elapsed
			// interval, plus noise scaled so the stationary std dev is dist.StdDev.
			decay := math.Exp(-now.Sub(lastSample).Seconds() / md.Walk.Seconds())
			current = dist.Mean + (current-dist.Mean)*decay +
				dist.StdDev*math.Sqrt(1-decay*decay)*rng.NormFloat64()
		}
		lastSample = now
		current = clampValue(current, md.Min, md.Max)
		value := current
		walkMu.Unlock()

		obs.Observe(value, attrs)
		return nil
	}
}

// clampValue restricts v to the optional [min, max] bounds.
func clampValue(v float64, minBound, maxBound *float64) float64 {
	if minBound != nil && v < *minBound {
		v = *minBound
	}
	if maxBound != nil && v > *maxBound {
		v = *maxBound
	}
	return v
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
// PeriodicReader. The Metrics API does not support caller-supplied timestamps;
// time offsets are applied at export time by NewTimeOffsetMetricExporter.
func (m *MetricObserver) Observe(info SpanInfo) {
	instruments := m.services[info.Service]
	if len(instruments) == 0 {
		return
	}

	for i := range instruments {
		inst := &instruments[i]

		if inst.operation != "" && inst.operation != info.Operation {
			continue
		}
		if inst.errorsOnly && !info.IsError {
			continue
		}

		// Lock only while sampling the RNG and building attributes.
		m.mu.Lock()
		attrs := buildMetricAttrs(inst.attrGens, info.Operation, m.rng)
		var sampledValue float64
		if dist, ok := m.effectiveValue(inst.scopeRef, inst.name, inst.value); ok {
			sampledValue = dist.Sample(m.rng)
		}
		m.mu.Unlock()

		switch {
		case inst.int64Counter != nil:
			inst.int64Counter.Add(context.Background(), 1, attrs)

		case inst.float64Counter != nil:
			inst.float64Counter.Add(context.Background(), max(0, sampledValue), attrs)

		case inst.int64UpDownCounter != nil:
			// -1 on span end (the +1 happens in ObserveStart)
			inst.int64UpDownCounter.Add(context.Background(), -1, attrs)

		case inst.float64UpDownCounter != nil:
			inst.float64UpDownCounter.Add(context.Background(), sampledValue, attrs)

		case inst.float64Histogram != nil:
			if inst.value != nil {
				inst.float64Histogram.Record(context.Background(), sampledValue, attrs)
			} else {
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

		m.mu.Lock()
		attrs := buildMetricAttrs(inst.attrGens, operation, m.rng)
		m.mu.Unlock()

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
