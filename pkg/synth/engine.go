// Simulation engine for walking the topology graph and emitting OTel spans
// Span timestamps are synthetic (no per-span sleeping); the outer loop sleeps for rate control
package synth

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Engine drives the trace generation simulation.
type Engine struct {
	Topology  *Topology
	Traffic   TrafficPattern
	Scenarios []Scenario
	Provider  *sdktrace.TracerProvider
	Rng       *rand.Rand
	Duration  time.Duration
}

// Stats holds counters collected during a simulation run.
type Stats struct {
	Traces       int64   `json:"traces"`
	Spans        int64   `json:"spans"`
	Errors       int64   `json:"errors"`
	ElapsedMs    int64   `json:"elapsed_ms"`
	TracesPerSec float64 `json:"traces_per_second"`
	SpansPerSec  float64 `json:"spans_per_second"`
	ErrorRate    float64 `json:"error_rate"`
}

// Run executes the main simulation loop with rate-controlled trace generation.
func (e *Engine) Run(ctx context.Context) (*Stats, error) {
	if len(e.Topology.Roots) == 0 {
		return nil, fmt.Errorf("no root operations to generate traces from")
	}

	var stats Stats
	startTime := time.Now()
	deadline := startTime.Add(e.Duration)

	for {
		select {
		case <-ctx.Done():
			e.finaliseStats(&stats, startTime)
			return &stats, nil
		default:
		}

		now := time.Now()
		if now.After(deadline) {
			e.finaliseStats(&stats, startTime)
			return &stats, nil
		}

		elapsed := now.Sub(startTime)
		rate := e.Traffic.Rate(elapsed)
		if rate <= 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Resolve active scenario overrides
		var overrides map[string]Override
		if len(e.Scenarios) > 0 {
			active := ActiveScenarios(e.Scenarios, elapsed)
			if len(active) > 0 {
				overrides = ResolveOverrides(active)
			}
		}

		// Pick a random root operation
		root := e.Topology.Roots[e.Rng.IntN(len(e.Topology.Roots))]

		// Walk the trace tree
		e.walkTrace(ctx, root, now, overrides, &stats)
		stats.Traces++

		// Sleep for the inter-arrival interval
		interval := time.Duration(float64(time.Second) / rate)
		select {
		case <-ctx.Done():
			e.finaliseStats(&stats, startTime)
			return &stats, nil
		case <-time.After(interval):
		}
	}
}

func (e *Engine) finaliseStats(stats *Stats, startTime time.Time) {
	elapsed := time.Since(startTime)
	stats.ElapsedMs = elapsed.Milliseconds()
	secs := elapsed.Seconds()
	if secs > 0 {
		stats.TracesPerSec = float64(stats.Traces) / secs
		stats.SpansPerSec = float64(stats.Spans) / secs
	}
	if stats.Spans > 0 {
		stats.ErrorRate = float64(stats.Errors) / float64(stats.Spans)
	}
}

// walkTrace recursively generates spans for an operation and its downstream calls.
// Synthetic timestamps are computed without sleeping.
func (e *Engine) walkTrace(ctx context.Context, op *Operation, startTime time.Time, overrides map[string]Override, stats *Stats) time.Time {
	tracer := e.Provider.Tracer(op.Service.Name)

	// Determine effective duration and error rate (apply overrides if active)
	duration := op.Duration
	errorRate := op.ErrorRate
	ref := op.Service.Name + "." + op.Name
	if ov, ok := overrides[ref]; ok {
		if ov.Duration.Mean > 0 {
			duration = ov.Duration
		}
		if ov.HasErrorRate {
			errorRate = ov.ErrorRate
		}
	}

	// Determine span kind: SERVER for roots, CLIENT for downstream calls
	kind := trace.SpanKindClient
	if isRoot(e.Topology, op) {
		kind = trace.SpanKindServer
	}

	ctx, span := tracer.Start(ctx, op.Name,
		trace.WithTimestamp(startTime),
		trace.WithSpanKind(kind),
		trace.WithAttributes(
			attribute.String("synth.service", op.Service.Name),
			attribute.String("synth.operation", op.Name),
		),
	)

	// Add service attributes
	for k, v := range op.Service.Attributes {
		span.SetAttributes(attribute.String(k, v))
	}

	// Add operation attributes
	for k, gen := range op.Attributes {
		span.SetAttributes(typedAttribute(k, gen.Generate(e.Rng)))
	}

	// Determine if this span errors
	isError := e.Rng.Float64() < errorRate

	// Sample own processing duration
	ownDuration := duration.Sample(e.Rng)

	// Pre-call work: half the own duration before calling downstream
	preCallDuration := ownDuration / 2
	childStartTime := startTime.Add(preCallDuration)

	// Walk downstream calls (parallel or sequential)
	latestChildEnd := childStartTime
	if op.CallStyle == "sequential" {
		nextStart := childStartTime
		for _, call := range op.Calls {
			childEnd := e.walkTrace(ctx, call.Operation, nextStart, overrides, stats)
			if childEnd.After(latestChildEnd) {
				latestChildEnd = childEnd
			}
			nextStart = childEnd
		}
	} else {
		for _, call := range op.Calls {
			childEnd := e.walkTrace(ctx, call.Operation, childStartTime, overrides, stats)
			if childEnd.After(latestChildEnd) {
				latestChildEnd = childEnd
			}
		}
	}

	// End time: max(child_end) + post-call overhead (remaining half of own duration)
	postCallDuration := ownDuration - preCallDuration
	endTime := latestChildEnd.Add(postCallDuration)

	if isError {
		span.SetStatus(codes.Error, "synthetic error")
		span.RecordError(fmt.Errorf("synthetic error"), trace.WithTimestamp(endTime))
		stats.Errors++
	}

	stats.Spans++
	span.End(trace.WithTimestamp(endTime))
	return endTime
}

// isRoot checks whether an operation is a root (entry point) in the topology.
func isRoot(topo *Topology, op *Operation) bool {
	return slices.Contains(topo.Roots, op)
}

// typedAttribute creates a KeyValue with the appropriate OTel type for the value.
func typedAttribute(key string, value any) attribute.KeyValue {
	switch v := value.(type) {
	case string:
		return attribute.String(key, v)
	case bool:
		return attribute.Bool(key, v)
	case int:
		return attribute.Int(key, v)
	case int64:
		return attribute.Int64(key, v)
	case float64:
		return attribute.Float64(key, v)
	default:
		return attribute.String(key, fmt.Sprint(v))
	}
}
