// Span planning for realtime emission mode.
// planTrace mirrors walkTrace but produces a []SpanPlan instead of OTel spans.
package synth

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// LinkRef holds the ref string and optional attributes for a span link.
type LinkRef struct {
	Ref        string
	Attributes []attribute.KeyValue
}

// SpanPlan holds pre-computed data for a single span, ready for deferred emission.
type SpanPlan struct {
	Index           int
	ParentIndex     int
	TraceID         trace.TraceID
	SpanID          trace.SpanID
	Service         string
	Operation       string
	Ref             string
	Kind            trace.SpanKind
	StartTime       time.Time
	EndTime         time.Time
	StartAttrs      []attribute.KeyValue
	Attrs           []attribute.KeyValue
	IsError         bool
	Scenarios       []string
	Rejected        bool
	RejectionReason string
	LinkRefs        []LinkRef
}

// planTrace recursively plans spans for an operation and its downstream calls.
// It mirrors walkTrace exactly: same RNG consumption order, same SimulationState
// mutations, same timing logic. The only difference is that it appends to plans
// instead of creating OTel spans.
// parent is the calling operation, nil for roots; it determines the span kind
// for same-service sync callees.
// Returns the span end time and whether the span errored.
func (e *Engine) planTrace(op, parent *Operation, parentIndex int, startTime time.Time, elapsed time.Duration, overrides map[string]Override, scenarioNames []string, stats *Stats, plans *[]SpanPlan, spanCount *int, spanLimit int, isAsync, isProducer bool) (time.Time, bool) {
	if *spanCount >= spanLimit {
		return startTime, false
	}
	*spanCount++

	index := len(*plans)

	duration := op.Duration
	errorRate := effectiveErrorRate(op, overrides)
	opAttrs := op.Attributes
	if ov, ok := overrides[op.Ref]; ok {
		if ov.Duration.Mean > 0 {
			duration = ov.Duration
		}
		if len(ov.Attributes) > 0 {
			opAttrs = op.Attributes.Merge(ov.Attributes)
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
				notifyPlanEvent(e.Observers, PlanEvent{Kind: PlanEventQueueRejection, Service: op.Service.Name, Operation: op.Name, Timestamp: startTime})
			case ReasonCircuitOpen:
				stats.CircuitBreakerTrips++
				notifyPlanEvent(e.Observers, PlanEvent{Kind: PlanEventCircuitBreakerTrip, Service: op.Service.Name, Operation: op.Name, Timestamp: startTime})
			}
			return e.planRejectionSpan(op, parent, parentIndex, startTime, reason, scenarioNames, plans, isAsync, isProducer)
		}
		if durationMult > 1.0 {
			duration.Mean = time.Duration(float64(duration.Mean) * durationMult)
		}
		errorRate = min(errorRate+errAdd, 1.0)
		opState.Enter()
	}

	kind := spanKindFor(e.Topology, op, parent, isAsync, isProducer)

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
	for _, a := range opAttrs {
		spanAttrs = append(spanAttrs, typedAttribute(a.Key, a.Gen.Generate(e.Rng)))
	}

	ownError := false
	if errorRate > 0 {
		if forced, ok := e.forcedChoice(choiceKindOperationError, op.Ref, "", -1); ok {
			ownError = forced
		} else {
			ownError = e.Rng.Float64() < errorRate
		}
	}
	ownDuration := duration.Sample(e.Rng)
	preCallDuration := ownDuration / 2
	childStartTime := startTime.Add(preCallDuration)

	var linkRefs []LinkRef
	for _, linked := range op.Links {
		linkRefs = append(linkRefs, LinkRef{
			Ref:        linked.Operation.Ref,
			Attributes: attributeKeyValues(linked.Attributes, e.Rng),
		})
	}

	// Append a placeholder plan entry; EndTime and IsError are filled in after children.
	plan := SpanPlan{
		Index:       index,
		ParentIndex: parentIndex,
		Service:     op.Service.Name,
		Operation:   op.Name,
		Ref:         op.Ref,
		Kind:        kind,
		StartTime:   startTime,
		StartAttrs:  startAttrs,
		Attrs:       spanAttrs,
		Scenarios:   scenarioNames,
		LinkRefs:    linkRefs,
	}
	*plans = append(*plans, plan)

	baseCalls := effectiveCalls(op, overrides)

	activeCalls := make([]activeCall, 0, len(baseCalls))
	for i, call := range baseCalls {
		if call.Condition == "on-error" && !ownError {
			continue
		}
		if call.Condition == "on-success" && ownError {
			continue
		}
		if call.Probability > 0 {
			fire, ok := false, false
			if isChoiceRate(call.Probability) {
				fire, ok = e.forcedChoice(choiceKindCallProbability, op.Ref, call.Operation.Ref, i)
			}
			if !ok {
				fire = e.Rng.Float64() < call.Probability
			}
			if !fire {
				continue
			}
		}
		activeCalls = append(activeCalls, activeCall{Call: call, ChoiceIndex: i})
	}

	latestChildEnd := childStartTime
	anyChildFailed := false
	if op.CallStyle == "sequential" {
		nextStart := childStartTime
		for _, active := range activeCalls {
			count := max(active.Call.Count, 1)
			for range count {
				perceivedEnd, failed := e.executePlanCall(active, op, index, nextStart, elapsed, overrides, scenarioNames, stats, plans, spanCount, spanLimit)
				if active.Call.Async {
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
		for _, active := range activeCalls {
			count := max(active.Call.Count, 1)
			for range count {
				perceivedEnd, failed := e.executePlanCall(active, op, index, childStartTime, elapsed, overrides, scenarioNames, stats, plans, spanCount, spanLimit)
				if active.Call.Async {
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
// The caller (planTrace) has already counted this span against the trace's
// span limit, so spanCount is not incremented here.
func (e *Engine) planRejectionSpan(op, parent *Operation, parentIndex int, startTime time.Time, reason string, scenarioNames []string, plans *[]SpanPlan, isAsync, isProducer bool) (time.Time, bool) {
	endTime := startTime.Add(rejectionDuration)

	kind := spanKindFor(e.Topology, op, parent, isAsync, isProducer)

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
		Ref:             op.Ref,
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
func (e *Engine) executePlanCall(active activeCall, parent *Operation, parentIndex int, callStart time.Time, elapsed time.Duration, overrides map[string]Override, scenarioNames []string, stats *Stats, plans *[]SpanPlan, spanCount *int, spanLimit int) (time.Time, bool) {
	call := active.Call
	maxAttempts := 1 + call.Retries
	attemptStart := callStart

	for attempt := range maxAttempts {
		childEnd, childErr := e.planTrace(call.Operation, parent, parentIndex, attemptStart, elapsed, overrides, scenarioNames, stats, plans, spanCount, spanLimit, call.Async, call.Producer)
		perceivedEnd := childEnd
		failed := childErr

		if call.Timeout > 0 && childEnd.Sub(attemptStart) > call.Timeout {
			perceivedEnd = attemptStart.Add(call.Timeout)
			failed = true
			stats.Timeouts++
			notifyPlanEvent(e.Observers, PlanEvent{Kind: PlanEventTimeout, Service: call.Operation.Service.Name, Operation: call.Operation.Name, Timestamp: perceivedEnd})
		}

		if attempt < maxAttempts-1 {
			if retry, ok := e.forcedChoice(choiceKindRetryActivation, parent.Ref, call.Operation.Ref, active.ChoiceIndex); ok {
				if !retry {
					return perceivedEnd, failed
				}
				failed = true
			}
		}

		if !failed || attempt == maxAttempts-1 {
			return perceivedEnd, failed
		}

		stats.Retries++
		notifyPlanEvent(e.Observers, PlanEvent{Kind: PlanEventRetry, Service: call.Operation.Service.Name, Operation: call.Operation.Name, Timestamp: perceivedEnd})
		attemptStart = perceivedEnd.Add(call.RetryBackoff)
	}

	return callStart, true
}
