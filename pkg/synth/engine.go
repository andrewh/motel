// Simulation engine for walking the topology graph and emitting OTel spans
// Span timestamps are synthetic (no per-span sleeping); the outer loop sleeps for rate control
package synth

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// DefaultMaxSpansPerTrace is the safety bound for span generation per trace.
const DefaultMaxSpansPerTrace = 10_000

// Engine drives the trace generation simulation.
type Engine struct {
	Topology         *Topology
	Traffic          TrafficPattern
	Scenarios        []Scenario
	Provider         *sdktrace.TracerProvider
	Rng              *rand.Rand
	Duration         time.Duration
	Observers        []SpanObserver
	MaxSpansPerTrace int
	State            *SimulationState
}

// Stats holds counters collected during a simulation run.
// Errors counts all spans in an error state, including those errored by cascading
// (a child failure marks its parent as errored too). ErrorRate is Errors/Spans.
// TraceErrorRate counts only traces where the root span errored.
type Stats struct {
	Traces              int64   `json:"traces"`
	Spans               int64   `json:"spans"`
	Errors              int64   `json:"errors"`
	FailedTraces        int64   `json:"failed_traces"`
	Timeouts            int64   `json:"timeouts"`
	Retries             int64   `json:"retries"`
	SpansBounded        int64   `json:"spans_bounded"`
	QueueRejections     int64   `json:"queue_rejections"`
	CircuitBreakerTrips int64   `json:"circuit_breaker_trips"`
	ElapsedMs           int64   `json:"elapsed_ms"`
	TracesPerSec        float64 `json:"traces_per_second"`
	SpansPerSec         float64 `json:"spans_per_second"`
	ErrorRate           float64 `json:"error_rate"`
	TraceErrorRate      float64 `json:"trace_error_rate"`
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

		// Resolve active scenario overrides (including traffic)
		var overrides map[string]Override
		trafficPattern := e.Traffic
		if len(e.Scenarios) > 0 {
			active := ActiveScenarios(e.Scenarios, elapsed)
			if len(active) > 0 {
				overrides = ResolveOverrides(active)
				if tp := ResolveTraffic(active); tp != nil {
					trafficPattern = tp
				}
			}
		}

		rate := trafficPattern.Rate(elapsed)
		if rate <= 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Pick a random root operation
		root := e.Topology.Roots[e.Rng.IntN(len(e.Topology.Roots))]

		// Walk the trace tree with a per-trace span counter
		spanLimit := e.maxSpansPerTrace()
		spanCount := 0
		_, rootErr := e.walkTrace(ctx, root, now, elapsed, overrides, &stats, &spanCount, spanLimit)
		stats.Traces++
		if rootErr {
			stats.FailedTraces++
		}
		if spanCount >= spanLimit {
			stats.SpansBounded++
		}

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
	if stats.Traces > 0 {
		stats.TraceErrorRate = float64(stats.FailedTraces) / float64(stats.Traces)
	}
}

func (e *Engine) maxSpansPerTrace() int {
	if e.MaxSpansPerTrace > 0 {
		return e.MaxSpansPerTrace
	}
	return DefaultMaxSpansPerTrace
}

// walkTrace recursively generates spans for an operation and its downstream calls.
// Returns the span end time and whether the span errored (own error rate or cascaded from children).
// spanCount tracks the number of spans generated in this trace; no new spans are created once it reaches spanLimit.
// elapsed is the simulation wall-clock time since engine start, used for state tracking.
func (e *Engine) walkTrace(ctx context.Context, op *Operation, startTime time.Time, elapsed time.Duration, overrides map[string]Override, stats *Stats, spanCount *int, spanLimit int) (time.Time, bool) {
	if *spanCount >= spanLimit {
		return startTime, false
	}
	*spanCount++
	tracer := e.Provider.Tracer(op.Service.Name)

	// Determine effective duration, error rate, and attributes (apply overrides if active)
	duration := op.Duration
	errorRate := op.ErrorRate
	opAttrs := op.Attributes
	ref := op.Service.Name + "." + op.Name
	if ov, ok := overrides[ref]; ok {
		if ov.Duration.Mean > 0 {
			duration = ov.Duration
		}
		if ov.HasErrorRate {
			errorRate = ov.ErrorRate
		}
		if len(ov.Attributes) > 0 {
			merged := make(map[string]AttributeGenerator, len(op.Attributes)+len(ov.Attributes))
			maps.Copy(merged, op.Attributes)
			maps.Copy(merged, ov.Attributes)
			opAttrs = merged
		}
	}

	// Consult simulation state for queue depth, circuit breaker, backpressure
	var opState *OperationState
	if e.State != nil {
		opState = e.State.Get(ref)
	}
	if opState != nil {
		durationMult, errAdd, rejected, reason := opState.Admit(elapsed, e.Rng)
		if rejected {
			switch reason {
			case ReasonQueueFull:
				stats.QueueRejections++
			case ReasonCircuitOpen:
				stats.CircuitBreakerTrips++
			}
			return e.emitRejectionSpan(ctx, op, startTime, reason, stats, spanCount)
		}
		if durationMult > 1.0 {
			duration.Mean = time.Duration(float64(duration.Mean) * durationMult)
		}
		errorRate = min(errorRate+errAdd, 1.0)
		opState.Enter()
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

	// Collect attributes for both the span and observers
	spanAttrs := make([]attribute.KeyValue, 0, len(op.Service.Attributes)+len(opAttrs))
	for k, v := range op.Service.Attributes {
		spanAttrs = append(spanAttrs, attribute.String(k, v))
	}
	for k, gen := range opAttrs {
		spanAttrs = append(spanAttrs, typedAttribute(k, gen.Generate(e.Rng)))
	}
	span.SetAttributes(spanAttrs...)

	// Determine if this span errors from its own error rate (before cascading)
	ownError := e.Rng.Float64() < errorRate

	// Sample own processing duration
	ownDuration := duration.Sample(e.Rng)

	// Pre-call work: half the own duration before calling downstream
	preCallDuration := ownDuration / 2
	childStartTime := startTime.Add(preCallDuration)

	// Filter calls by condition and probability (uses own error state, not cascaded)
	activeCalls := make([]Call, 0, len(op.Calls))
	for _, call := range op.Calls {
		if call.Condition == "on-error" && !ownError {
			continue
		}
		if call.Condition == "on-success" && ownError {
			continue
		}
		if call.Probability > 0 && e.Rng.Float64() >= call.Probability {
			continue
		}
		activeCalls = append(activeCalls, call)
	}

	// Walk downstream calls (parallel or sequential) with fan-out
	latestChildEnd := childStartTime
	anyChildFailed := false
	if op.CallStyle == "sequential" {
		nextStart := childStartTime
		for _, call := range activeCalls {
			count := max(call.Count, 1)
			for range count {
				perceivedEnd, failed := e.executeCall(ctx, call, nextStart, elapsed, overrides, stats, spanCount, spanLimit)
				if failed {
					anyChildFailed = true
				}
				if perceivedEnd.After(latestChildEnd) {
					latestChildEnd = perceivedEnd
				}
				nextStart = perceivedEnd
			}
		}
	} else {
		for _, call := range activeCalls {
			count := max(call.Count, 1)
			for range count {
				perceivedEnd, failed := e.executeCall(ctx, call, childStartTime, elapsed, overrides, stats, spanCount, spanLimit)
				if failed {
					anyChildFailed = true
				}
				if perceivedEnd.After(latestChildEnd) {
					latestChildEnd = perceivedEnd
				}
			}
		}
	}

	// End time: max(child_end) + post-call overhead (remaining half of own duration)
	postCallDuration := ownDuration - preCallDuration
	endTime := latestChildEnd.Add(postCallDuration)

	// Cascade child failures to parent
	isError := ownError || anyChildFailed

	if isError {
		span.SetStatus(codes.Error, "synthetic error")
		span.RecordError(fmt.Errorf("synthetic error"), trace.WithTimestamp(endTime))
		stats.Errors++
	}

	stats.Spans++
	span.End(trace.WithTimestamp(endTime))

	if opState != nil {
		opState.Exit(elapsed, endTime.Sub(startTime), isError)
	}

	if len(e.Observers) > 0 {
		attrsCopy := make([]attribute.KeyValue, len(spanAttrs))
		copy(attrsCopy, spanAttrs)
		info := SpanInfo{
			Service:   op.Service.Name,
			Operation: op.Name,
			Duration:  endTime.Sub(startTime),
			IsError:   isError,
			Kind:      kind,
			Attrs:     attrsCopy,
		}
		for _, obs := range e.Observers {
			obs.Observe(info)
		}
	}

	return endTime, isError
}

// emitRejectionSpan creates a short error span for a rejected request.
func (e *Engine) emitRejectionSpan(ctx context.Context, op *Operation, startTime time.Time, reason string, stats *Stats, spanCount *int) (time.Time, bool) {
	*spanCount++
	tracer := e.Provider.Tracer(op.Service.Name)
	endTime := startTime.Add(rejectionDuration)

	kind := trace.SpanKindClient
	if isRoot(e.Topology, op) {
		kind = trace.SpanKindServer
	}

	_, span := tracer.Start(ctx, op.Name,
		trace.WithTimestamp(startTime),
		trace.WithSpanKind(kind),
		trace.WithAttributes(
			attribute.String("synth.service", op.Service.Name),
			attribute.String("synth.operation", op.Name),
			attribute.Bool("synth.rejected", true),
			attribute.String("synth.rejection_reason", reason),
		),
	)
	span.SetStatus(codes.Error, reason)
	span.RecordError(fmt.Errorf("rejected: %s", reason), trace.WithTimestamp(endTime))
	span.End(trace.WithTimestamp(endTime))

	stats.Spans++
	stats.Errors++

	if len(e.Observers) > 0 {
		info := SpanInfo{
			Service:   op.Service.Name,
			Operation: op.Name,
			Duration:  rejectionDuration,
			IsError:   true,
			Kind:      kind,
			Attrs: []attribute.KeyValue{
				attribute.Bool("synth.rejected", true),
				attribute.String("synth.rejection_reason", reason),
			},
		}
		for _, obs := range e.Observers {
			obs.Observe(info)
		}
	}

	return endTime, true
}

// executeCall runs a single downstream call, applying timeout capping and retries.
func (e *Engine) executeCall(ctx context.Context, call Call, callStart time.Time, elapsed time.Duration, overrides map[string]Override, stats *Stats, spanCount *int, spanLimit int) (time.Time, bool) {
	maxAttempts := 1 + call.Retries
	attemptStart := callStart

	for attempt := range maxAttempts {
		childEnd, childErr := e.walkTrace(ctx, call.Operation, attemptStart, elapsed, overrides, stats, spanCount, spanLimit)
		perceivedEnd := childEnd
		failed := childErr

		if call.Timeout > 0 && childEnd.Sub(attemptStart) > call.Timeout {
			perceivedEnd = attemptStart.Add(call.Timeout)
			failed = true
			stats.Timeouts++
		}

		if !failed || attempt == maxAttempts-1 {
			return perceivedEnd, failed
		}

		stats.Retries++
		attemptStart = perceivedEnd.Add(call.RetryBackoff)
	}

	return callStart, true // unreachable: loop always returns on final iteration
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
