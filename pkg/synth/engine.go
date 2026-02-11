// Simulation engine for walking the topology graph and emitting OTel spans
// Generates synthetic traces with realistic timestamps without sleeping
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

// Run executes the main simulation loop with rate-controlled trace generation.
func (e *Engine) Run(ctx context.Context) error {
	if len(e.Topology.Roots) == 0 {
		return fmt.Errorf("no root operations to generate traces from")
	}

	startTime := time.Now()
	deadline := startTime.Add(e.Duration)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		now := time.Now()
		if now.After(deadline) {
			return nil
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
		e.walkTrace(ctx, root, now, overrides)

		// Sleep for the inter-arrival interval
		interval := time.Duration(float64(time.Second) / rate)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// walkTrace recursively generates spans for an operation and its downstream calls.
// Synthetic timestamps are computed without sleeping.
func (e *Engine) walkTrace(ctx context.Context, op *Operation, startTime time.Time, overrides map[string]Override) time.Time {
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

	// Determine if this span errors
	isError := e.Rng.Float64() < errorRate

	// Sample own processing duration
	ownDuration := duration.Sample(e.Rng)

	// Pre-call work: half the own duration before calling downstream
	preCallDuration := ownDuration / 2
	childStartTime := startTime.Add(preCallDuration)

	// Walk downstream calls
	latestChildEnd := childStartTime
	for _, child := range op.Calls {
		childEnd := e.walkTrace(ctx, child, childStartTime, overrides)
		if childEnd.After(latestChildEnd) {
			latestChildEnd = childEnd
		}
	}

	// End time: max(child_end) + post-call overhead (remaining half of own duration)
	postCallDuration := ownDuration - preCallDuration
	endTime := latestChildEnd.Add(postCallDuration)

	if isError {
		span.SetStatus(codes.Error, "synthetic error")
		span.RecordError(fmt.Errorf("synthetic error"), trace.WithTimestamp(endTime))
	}

	span.End(trace.WithTimestamp(endTime))
	return endTime
}

// isRoot checks whether an operation is a root (entry point) in the topology.
func isRoot(topo *Topology, op *Operation) bool {
	return slices.Contains(topo.Roots, op)
}
