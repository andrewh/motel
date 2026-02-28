// Span planning for realtime emission mode.
// planTrace mirrors walkTrace but produces a []SpanPlan instead of OTel spans.
package synth

import (
	"maps"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SpanPlan holds pre-computed data for a single span, ready for deferred emission.
type SpanPlan struct {
	Index           int
	ParentIndex     int
	Service         string
	Operation       string
	Kind            trace.SpanKind
	StartTime       time.Time
	EndTime         time.Time
	StartAttrs      []attribute.KeyValue
	Attrs           []attribute.KeyValue
	IsError         bool
	Scenarios       []string
	Rejected        bool
	RejectionReason string
}

// planTrace recursively plans spans for an operation and its downstream calls.
// It mirrors walkTrace exactly: same RNG consumption order, same SimulationState
// mutations, same timing logic. The only difference is that it appends to plans
// instead of creating OTel spans.
// Returns the span end time and whether the span errored.
func (e *Engine) planTrace(op *Operation, parentIndex int, startTime time.Time, elapsed time.Duration, overrides map[string]Override, scenarioNames []string, stats *Stats, plans *[]SpanPlan, spanCount *int, spanLimit int) (time.Time, bool) {
	if *spanCount >= spanLimit {
		return startTime, false
	}
	*spanCount++

	index := len(*plans)

	duration := op.Duration
	errorRate := op.ErrorRate
	opAttrs := op.Attributes
	if ov, ok := overrides[op.Ref]; ok {
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

	var opState *OperationState
	if e.State != nil {
		opState = e.State.Get(op.Ref)
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
			return e.planRejectionSpan(op, parentIndex, startTime, reason, scenarioNames, plans, spanCount)
		}
		if durationMult > 1.0 {
			duration.Mean = time.Duration(float64(duration.Mean) * durationMult)
		}
		errorRate = min(errorRate+errAdd, 1.0)
		opState.Enter()
	}

	kind := trace.SpanKindClient
	if isRoot(e.Topology, op) {
		kind = trace.SpanKindServer
	}

	startAttrs := []attribute.KeyValue{
		attribute.String("synth.service", op.Service.Name),
		attribute.String("synth.operation", op.Name),
	}
	if e.LabelScenarios {
		startAttrs = append(startAttrs, attribute.StringSlice("synth.scenarios", scenarioNames))
	}

	spanAttrs := make([]attribute.KeyValue, 0, len(op.Service.Attributes)+len(opAttrs))
	for k, v := range op.Service.Attributes {
		spanAttrs = append(spanAttrs, attribute.String(k, v))
	}
	for k, gen := range opAttrs {
		spanAttrs = append(spanAttrs, typedAttribute(k, gen.Generate(e.Rng)))
	}

	ownError := e.Rng.Float64() < errorRate
	ownDuration := duration.Sample(e.Rng)
	preCallDuration := ownDuration / 2
	childStartTime := startTime.Add(preCallDuration)

	// Append a placeholder plan entry; EndTime and IsError are filled in after children.
	plan := SpanPlan{
		Index:       index,
		ParentIndex: parentIndex,
		Service:     op.Service.Name,
		Operation:   op.Name,
		Kind:        kind,
		StartTime:   startTime,
		StartAttrs:  startAttrs,
		Attrs:       spanAttrs,
		Scenarios:   scenarioNames,
	}
	*plans = append(*plans, plan)

	baseCalls := effectiveCalls(op, overrides)

	activeCalls := make([]Call, 0, len(baseCalls))
	for _, call := range baseCalls {
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

	latestChildEnd := childStartTime
	anyChildFailed := false
	if op.CallStyle == "sequential" {
		nextStart := childStartTime
		for _, call := range activeCalls {
			count := max(call.Count, 1)
			for range count {
				perceivedEnd, failed := e.executePlanCall(call, index, nextStart, elapsed, overrides, scenarioNames, stats, plans, spanCount, spanLimit)
				if call.Async {
					continue
				}
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
				perceivedEnd, failed := e.executePlanCall(call, index, childStartTime, elapsed, overrides, scenarioNames, stats, plans, spanCount, spanLimit)
				if call.Async {
					continue
				}
				if failed {
					anyChildFailed = true
				}
				if perceivedEnd.After(latestChildEnd) {
					latestChildEnd = perceivedEnd
				}
			}
		}
	}

	postCallDuration := ownDuration - preCallDuration
	endTime := latestChildEnd.Add(postCallDuration)

	isError := ownError || anyChildFailed

	// Fill in the deferred fields now that children are resolved.
	(*plans)[index].EndTime = endTime
	(*plans)[index].IsError = isError

	if opState != nil {
		opState.Exit(elapsed, endTime.Sub(startTime), isError)
	}

	return endTime, isError
}

// planRejectionSpan mirrors emitRejectionSpan but appends to plans.
func (e *Engine) planRejectionSpan(op *Operation, parentIndex int, startTime time.Time, reason string, scenarioNames []string, plans *[]SpanPlan, spanCount *int) (time.Time, bool) {
	*spanCount++
	endTime := startTime.Add(rejectionDuration)

	kind := trace.SpanKindClient
	if isRoot(e.Topology, op) {
		kind = trace.SpanKindServer
	}

	rejAttrs := []attribute.KeyValue{
		attribute.String("synth.service", op.Service.Name),
		attribute.String("synth.operation", op.Name),
		attribute.Bool("synth.rejected", true),
		attribute.String("synth.rejection_reason", reason),
	}
	if e.LabelScenarios {
		rejAttrs = append(rejAttrs, attribute.StringSlice("synth.scenarios", scenarioNames))
	}

	*plans = append(*plans, SpanPlan{
		Index:           len(*plans),
		ParentIndex:     parentIndex,
		Service:         op.Service.Name,
		Operation:       op.Name,
		Kind:            kind,
		StartTime:       startTime,
		EndTime:         endTime,
		StartAttrs:      rejAttrs,
		IsError:         true,
		Rejected:        true,
		RejectionReason: reason,
		Scenarios:       scenarioNames,
	})

	return endTime, true
}

// executePlanCall mirrors executeCall but delegates to planTrace.
func (e *Engine) executePlanCall(call Call, parentIndex int, callStart time.Time, elapsed time.Duration, overrides map[string]Override, scenarioNames []string, stats *Stats, plans *[]SpanPlan, spanCount *int, spanLimit int) (time.Time, bool) {
	maxAttempts := 1 + call.Retries
	attemptStart := callStart

	for attempt := range maxAttempts {
		childEnd, childErr := e.planTrace(call.Operation, parentIndex, attemptStart, elapsed, overrides, scenarioNames, stats, plans, spanCount, spanLimit)
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

	return callStart, true
}
