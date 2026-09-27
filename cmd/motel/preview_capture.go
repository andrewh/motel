package main

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	maxPreviewRunDuration = 10 * time.Second
	maxPreviewTraces      = 200
	maxPreviewSpans       = 1000
	maxPreviewMetrics     = 500
	maxPreviewLogs        = 500
)

type previewRunOptions struct {
	duration      time.Duration
	slowThreshold time.Duration
	seed          uint64
	maxTraces     int
}

type previewAttribute struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

func (o previewRunOptions) validate() error {
	if o.duration <= 0 || o.duration > maxPreviewRunDuration {
		return fmt.Errorf("--run-duration must be greater than zero and at most %s", maxPreviewRunDuration)
	}
	if o.maxTraces <= 0 || o.maxTraces > maxPreviewTraces {
		return fmt.Errorf("--max-traces must be between 1 and %d", maxPreviewTraces)
	}
	if o.slowThreshold < 0 {
		return fmt.Errorf("--slow-threshold must not be negative, got %s", o.slowThreshold)
	}
	return nil
}

type previewSpan struct {
	TraceID      string                      `json:"trace_id"`
	SpanID       string                      `json:"span_id"`
	ParentSpanID string                      `json:"parent_span_id,omitempty"`
	Service      string                      `json:"service"`
	Name         string                      `json:"name"`
	Kind         string                      `json:"kind"`
	StartTime    time.Time                   `json:"start_time"`
	EndTime      time.Time                   `json:"end_time"`
	StartMs      int64                       `json:"start_ms"`
	DurationMs   float64                     `json:"duration_ms"`
	Status       string                      `json:"status"`
	Resource     map[string]previewAttribute `json:"resource,omitempty"`
	Attributes   map[string]previewAttribute `json:"attributes,omitempty"`
	Events       []previewEvent              `json:"events,omitempty"`
	Links        []previewLink               `json:"links,omitempty"`
}

type previewEvent struct {
	Name       string                      `json:"name"`
	TimeMs     int64                       `json:"time_ms"`
	Attributes map[string]previewAttribute `json:"attributes,omitempty"`
}

type previewLink struct {
	TraceID    string                      `json:"trace_id"`
	SpanID     string                      `json:"span_id"`
	Attributes map[string]previewAttribute `json:"attributes,omitempty"`
}

type previewMetric struct {
	Name       string                      `json:"name"`
	Type       string                      `json:"type"`
	Unit       string                      `json:"unit,omitempty"`
	Service    string                      `json:"service"`
	Resource   map[string]previewAttribute `json:"resource,omitempty"`
	Value      string                      `json:"value"`
	TimeMs     int64                       `json:"time_ms"`
	StartMs    int64                       `json:"start_ms"`
	Count      uint64                      `json:"count,omitempty"`
	Sum        float64                     `json:"sum,omitempty"`
	Min        *float64                    `json:"min,omitempty"`
	Max        *float64                    `json:"max,omitempty"`
	Bounds     []float64                   `json:"bounds,omitempty"`
	Buckets    []uint64                    `json:"buckets,omitempty"`
	Attributes map[string]previewAttribute `json:"attributes,omitempty"`
}

type previewLog struct {
	TimeMs     int64                       `json:"time_ms"`
	Service    string                      `json:"service"`
	Resource   map[string]previewAttribute `json:"resource,omitempty"`
	Severity   string                      `json:"severity"`
	Body       string                      `json:"body"`
	TraceID    string                      `json:"trace_id,omitempty"`
	SpanID     string                      `json:"span_id,omitempty"`
	Attributes map[string]previewAttribute `json:"attributes,omitempty"`
}

type previewCapture struct {
	Duration      string          `json:"duration"`
	SlowThreshold string          `json:"slow_threshold"`
	Seed          uint64          `json:"seed"`
	MaxTraces     int             `json:"max_traces"`
	Stats         *synth.Stats    `json:"stats"`
	Spans         []previewSpan   `json:"spans"`
	Metrics       []previewMetric `json:"metrics"`
	Logs          []previewLog    `json:"logs"`
	DroppedSpans  int             `json:"dropped_spans"`
}

type previewSpanExporter struct {
	mu      sync.Mutex
	spans   []previewSpan
	dropped int
}

func (e *previewSpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, span := range spans {
		if len(e.spans) >= maxPreviewSpans {
			e.dropped++
			continue
		}
		attrs := spanAttributes(span.Attributes())
		service := resourceService(span.Resource())
		if service == "" {
			service, _ = attrs["synth.service"].Value.(string)
		}
		parentID := span.Parent().SpanID().String()
		if !span.Parent().IsValid() {
			parentID = ""
		}
		item := previewSpan{
			TraceID:      span.SpanContext().TraceID().String(),
			SpanID:       span.SpanContext().SpanID().String(),
			ParentSpanID: parentID,
			Service:      service,
			Name:         span.Name(),
			Kind:         span.SpanKind().String(),
			StartTime:    span.StartTime(),
			EndTime:      span.EndTime(),
			StartMs:      span.StartTime().UnixMilli(),
			DurationMs:   float64(span.EndTime().Sub(span.StartTime()).Microseconds()) / 1000,
			Status:       span.Status().Code.String(),
			Resource:     previewResourceAttributes(span.Resource()),
			Attributes:   attrs,
		}
		for _, event := range span.Events() {
			item.Events = append(item.Events, previewEvent{Name: event.Name, TimeMs: event.Time.UnixMilli(), Attributes: spanAttributes(event.Attributes)})
		}
		for _, link := range span.Links() {
			item.Links = append(item.Links, previewLink{TraceID: link.SpanContext.TraceID().String(), SpanID: link.SpanContext.SpanID().String(), Attributes: spanAttributes(link.Attributes)})
		}
		e.spans = append(e.spans, item)
	}
	return nil
}

func (e *previewSpanExporter) Shutdown(context.Context) error { return nil }

func (e *previewSpanExporter) Records() ([]previewSpan, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.spans), e.dropped
}

type previewLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *previewLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, record := range records {
		if len(e.records) >= maxPreviewLogs {
			break
		}
		e.records = append(e.records, record.Clone())
	}
	return nil
}

func (e *previewLogExporter) Shutdown(context.Context) error   { return nil }
func (e *previewLogExporter) ForceFlush(context.Context) error { return nil }

func (e *previewLogExporter) Records() []previewLog {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]previewLog, 0, len(e.records))
	for _, record := range e.records {
		attrs := make(map[string]previewAttribute, record.AttributesLen())
		record.WalkAttributes(func(kv attribute.KeyValue) bool {
			attrs[string(kv.Key)] = previewAttribute{Type: kv.Value.Type().String(), Value: kv.Value.AsInterface()}
			return true
		})
		item := previewLog{
			TimeMs:     record.Timestamp().UnixMilli(),
			Service:    resourceService(record.Resource()),
			Resource:   previewResourceAttributes(record.Resource()),
			Severity:   record.SeverityText(),
			Body:       record.Body().AsString(),
			TraceID:    record.TraceID().String(),
			SpanID:     record.SpanID().String(),
			Attributes: attrs,
		}
		if item.Severity == "" {
			item.Severity = record.Severity().String()
		}
		result = append(result, item)
	}
	return result
}

func capturePreview(topo *synth.Topology, traffic synth.TrafficPattern, scenarios []synth.Scenario, opts previewRunOptions) (*previewCapture, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.duration+2*time.Second)
	defer cancel()

	spans := &previewSpanExporter{}
	traceProviders := make(map[string]*sdktrace.TracerProvider, len(topo.Services))

	meters := make(map[string]metric.Meter, len(topo.Services))
	metricReaders := make([]*sdkmetric.ManualReader, 0, len(topo.Services))
	metricProviders := make([]*sdkmetric.MeterProvider, 0, len(topo.Services))
	loggers := make(map[string]log.Logger, len(topo.Services))
	logProviders := make([]*sdklog.LoggerProvider, 0, len(topo.Services))
	logs := &previewLogExporter{}
	for _, name := range sortedServiceNames(topo) {
		attrs := make([]attribute.KeyValue, 0, 1+len(topo.Services[name].ResourceAttributes))
		attrs = append(attrs, attribute.String("service.name", name))
		for key, value := range topo.Services[name].ResourceAttributes {
			attrs = append(attrs, attribute.String(key, value))
		}
		res := resource.NewSchemaless(attrs...)
		traceProviders[name] = sdktrace.NewTracerProvider(
			sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(spans)),
			sdktrace.WithResource(res),
		)
		reader := sdkmetric.NewManualReader()
		meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
		metricReaders = append(metricReaders, reader)
		metricProviders = append(metricProviders, meterProvider)
		meters[name] = meterProvider.Meter("motel")
		loggerProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(logs)), sdklog.WithResource(res))
		logProviders = append(logProviders, loggerProvider)
		loggers[name] = loggerProvider.Logger("motel")
	}
	defer func() {
		for _, provider := range traceProviders {
			_ = provider.Shutdown(context.Background())
		}
		for _, provider := range metricProviders {
			_ = provider.Shutdown(context.Background())
		}
		for _, provider := range logProviders {
			_ = provider.Shutdown(context.Background())
		}
	}()
	tracers, err := tracerSource(topo, traceProviders)
	if err != nil {
		return nil, fmt.Errorf("creating preview tracers: %w", err)
	}

	metricObserver, err := synth.NewMetricObserver(meters, topo, rand.New(rand.NewPCG(opts.seed^0xa0761d6478bd642f, opts.seed^0xe7037ed1a0b428db)))
	if err != nil {
		return nil, fmt.Errorf("creating preview metric observer: %w", err)
	}
	stopMetrics := metricObserver.Start()
	defer func() {
		if stopMetrics != nil {
			stopMetrics()
		}
	}()
	logObserver, err := synth.NewLogObserver(loggers, topo, opts.slowThreshold, rand.New(rand.NewPCG(opts.seed^0x8ebc6af09c88c6e3, opts.seed^0x589965cc75374cc3)))
	if err != nil {
		return nil, fmt.Errorf("creating preview log observer: %w", err)
	}
	engine := &synth.Engine{
		Topology:         topo,
		Traffic:          traffic,
		Scenarios:        scenarios,
		Tracers:          tracers,
		Rng:              rand.New(rand.NewPCG(opts.seed, opts.seed^0x9e3779b97f4a7c15)),
		Duration:         opts.duration,
		Observers:        []synth.SpanObserver{metricObserver, logObserver},
		MaxSpansPerTrace: synth.DefaultMaxSpansPerTrace,
		MaxTraces:        opts.maxTraces,
		State:            synth.NewSimulationState(topo),
		LabelScenarios:   true,
	}
	stats, err := engine.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("running preview simulation: %w", err)
	}
	stopMetrics()
	stopMetrics = nil
	result := &previewCapture{
		Duration:      opts.duration.String(),
		SlowThreshold: opts.slowThreshold.String(),
		Seed:          opts.seed,
		MaxTraces:     opts.maxTraces,
		Stats:         stats,
		Spans:         []previewSpan{},
		Metrics:       []previewMetric{},
		Logs:          []previewLog{},
	}
	capturedSpans, droppedSpans := spans.Records()
	result.Spans = append(result.Spans, capturedSpans...)
	result.DroppedSpans = droppedSpans
	for _, reader := range metricReaders {
		if len(result.Metrics) >= maxPreviewMetrics {
			break
		}
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(ctx, &rm); err != nil {
			return nil, fmt.Errorf("collecting preview metrics: %w", err)
		}
		result.Metrics = append(result.Metrics, previewMetricRecords(rm, maxPreviewMetrics-len(result.Metrics))...)
	}
	for _, provider := range logProviders {
		if err := provider.ForceFlush(ctx); err != nil {
			return nil, fmt.Errorf("flushing preview logs: %w", err)
		}
	}
	result.Logs = logs.Records()
	slices.SortFunc(result.Spans, func(a, b previewSpan) int {
		if a.StartMs != b.StartMs {
			return cmp.Compare(a.StartMs, b.StartMs)
		}
		return cmp.Compare(a.SpanID, b.SpanID)
	})
	return result, nil
}

func spanAttributes(attrs []attribute.KeyValue) map[string]previewAttribute {
	if len(attrs) == 0 {
		return nil
	}
	result := make(map[string]previewAttribute, len(attrs))
	for _, attr := range attrs {
		result[string(attr.Key)] = previewAttribute{Type: attr.Value.Type().String(), Value: attr.Value.AsInterface()}
	}
	return result
}

func resourceService(res *resource.Resource) string {
	if res == nil {
		return ""
	}
	value, ok := res.Set().Value(attribute.Key("service.name"))
	if !ok {
		return ""
	}
	return value.AsString()
}

func previewResourceAttributes(res *resource.Resource) map[string]previewAttribute {
	if res == nil {
		return nil
	}
	return spanAttributes(res.Attributes())
}

func previewMetricRecords(rm metricdata.ResourceMetrics, limit int) []previewMetric {
	service := resourceService(rm.Resource)
	resourceAttrs := previewResourceAttributes(rm.Resource)
	var result []previewMetric
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if len(result) >= limit {
				return result
			}
			start := len(result)
			switch data := metric.Data.(type) {
			case metricdata.Gauge[int64]:
				result = append(result, previewNumberMetrics(metric.Name, "gauge", metric.Unit, service, data.DataPoints, limit-len(result))...)
			case metricdata.Gauge[float64]:
				result = append(result, previewNumberMetrics(metric.Name, "gauge", metric.Unit, service, data.DataPoints, limit-len(result))...)
			case metricdata.Sum[int64]:
				result = append(result, previewNumberMetrics(metric.Name, "sum", metric.Unit, service, data.DataPoints, limit-len(result))...)
			case metricdata.Sum[float64]:
				result = append(result, previewNumberMetrics(metric.Name, "sum", metric.Unit, service, data.DataPoints, limit-len(result))...)
			case metricdata.Histogram[int64]:
				result = append(result, previewHistogramMetrics(metric.Name, metric.Unit, service, data.DataPoints, limit-len(result))...)
			case metricdata.Histogram[float64]:
				result = append(result, previewHistogramMetrics(metric.Name, metric.Unit, service, data.DataPoints, limit-len(result))...)
			}
			for i := start; i < len(result); i++ {
				result[i].Resource = resourceAttrs
			}
		}
	}
	return result
}

func previewNumberMetrics[N int64 | float64](name, kind, unit, service string, points []metricdata.DataPoint[N], limit int) []previewMetric {
	var result []previewMetric
	for _, point := range points {
		if len(result) >= limit {
			break
		}
		result = append(result, previewMetric{
			Name:       name,
			Type:       kind,
			Unit:       unit,
			Service:    service,
			Value:      fmt.Sprint(point.Value),
			TimeMs:     point.Time.UnixMilli(),
			StartMs:    point.StartTime.UnixMilli(),
			Attributes: metricAttributes(point.Attributes),
		})
	}
	return result
}

func previewHistogramMetrics[N int64 | float64](name, unit, service string, points []metricdata.HistogramDataPoint[N], limit int) []previewMetric {
	var result []previewMetric
	for _, point := range points {
		if len(result) >= limit {
			break
		}
		item := previewMetric{
			Name:       name,
			Type:       "histogram",
			Unit:       unit,
			Service:    service,
			Value:      fmt.Sprintf("count %d · sum %v", point.Count, point.Sum),
			TimeMs:     point.Time.UnixMilli(),
			StartMs:    point.StartTime.UnixMilli(),
			Count:      point.Count,
			Sum:        float64(point.Sum),
			Bounds:     slices.Clone(point.Bounds),
			Buckets:    slices.Clone(point.BucketCounts),
			Attributes: metricAttributes(point.Attributes),
		}
		if value, defined := point.Min.Value(); defined {
			minValue := float64(value)
			item.Min = &minValue
		}
		if value, defined := point.Max.Value(); defined {
			maxValue := float64(value)
			item.Max = &maxValue
		}
		result = append(result, item)
	}
	return result
}

func metricAttributes(set attribute.Set) map[string]previewAttribute {
	if set.Len() == 0 {
		return nil
	}
	result := make(map[string]previewAttribute, set.Len())
	for iter := set.Iter(); iter.Next(); {
		item := iter.Attribute()
		result[string(item.Key)] = previewAttribute{Type: item.Value.Type().String(), Value: item.Value.AsInterface()}
	}
	return result
}
