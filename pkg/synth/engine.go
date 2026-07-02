// Package synth generates synthetic OpenTelemetry signals from a topology graph.
// It provides a simulation engine, traffic patterns, attribute generators,
// and structural analysis tools for testing observability pipelines.
package synth

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// DefaultMaxSpansPerTrace is the safety bound for span generation per trace.
const DefaultMaxSpansPerTrace = 10_000

const zeroRateIdleInterval = 10 * time.Millisecond

// spanContextRegistry stores the most recent span context for each operation ref.
// Used to attach cross-trace span links from consumer operations to producer operations.
// Concurrent Store calls produce last-writer-wins semantics — "most recent" is
// approximate when multiple goroutines emit spans for the same operation.
type spanContextRegistry struct {
	mu      sync.RWMutex
	ctx     map[string]trace.SpanContext
	targets map[string]bool // only store contexts for operations referenced as link targets
}

func (r *spanContextRegistry) store(ref string, sc trace.SpanContext) {
	if !r.targets[ref] {
		return
	}
	r.mu.Lock()
	r.ctx[ref] = sc
	r.mu.Unlock()
}

func (r *spanContextRegistry) load(ref string) (trace.SpanContext, bool) {
	r.mu.RLock()
	sc, ok := r.ctx[ref]
	r.mu.RUnlock()
	return sc, ok
}

// newSpanContextRegistry creates a registry that only stores contexts for
// operations referenced as link targets in the topology.
func newSpanContextRegistry(topo *Topology) *spanContextRegistry {
	targets := make(map[string]bool)
	for _, svc := range topo.Services {
		for _, op := range svc.Operations {
			for _, linked := range op.Links {
				targets[linked.Operation.Ref] = true
			}
		}
	}
	return &spanContextRegistry{
		ctx:     make(map[string]trace.SpanContext),
		targets: targets,
	}
}

// DefaultMaxInFlightTraces limits concurrent trace emission in realtime mode.
const DefaultMaxInFlightTraces = 1000

// TracerSource returns a trace.Tracer for the named service.
// The engine calls this for every span, so implementations should be cheap
// (e.g. a map lookup or a method value on a single TracerProvider).
type TracerSource func(serviceName string) trace.Tracer

// Engine drives the trace generation simulation.
type Engine struct {
	Topology          *Topology
	Traffic           TrafficPattern
	Scenarios         []Scenario
	Tracers           TracerSource
	Rng               *rand.Rand
	Duration          time.Duration
	Observers         []SpanObserver
	MaxSpansPerTrace  int
	State             *SimulationState
	LabelScenarios    bool
	TimeOffset        time.Duration
	Realtime          bool
	MaxInFlightTraces int
	MaxTraces         int
	linkRegistry      *spanContextRegistry
	choiceDecisions   choiceDecisions
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

	e.linkRegistry = newSpanContextRegistry(e.Topology)

	if e.Realtime {
		return e.runRealtime(ctx)
	}

	var stats Stats
	startTime := time.Now()
	deadline := startTime.Add(e.Duration)
	var lastActive []Scenario

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
		var scenarioNames []string
		trafficPattern := e.Traffic
		if len(e.Scenarios) > 0 {
			active := ActiveScenarios(e.Scenarios, elapsed)
			if len(active) > 0 {
				overrides = ResolveOverrides(active)
				if tp := ResolveTraffic(active); tp != nil {
					trafficPattern = tp
				}
				if e.LabelScenarios {
					scenarioNames = make([]string, len(active))
					for i, s := range active {
						scenarioNames[i] = s.Name
					}
				}
			}
			// Scenario contents are static, so the merged overrides only
			// change when the active set does — notify observers on
			// transitions rather than every iteration.
			if !activeScenariosEqual(active, lastActive) {
				notifyOverrides(e.Observers, overrides)
				lastActive = active
			}
		}

		rate := trafficPattern.Rate(elapsed)
		if rate <= 0 {
			if waitZeroRate(ctx) {
				e.finaliseStats(&stats, startTime)
				return &stats, nil
			}
			continue
		}

		// Pick a random root operation
		root := e.Topology.Roots[e.Rng.IntN(len(e.Topology.Roots))]

		// Walk the trace tree with a per-trace span counter.
		// Shift span start times by TimeOffset so exported timestamps appear
		// in the past or future, while scenario timing uses real elapsed time.
		spanStart := now.Add(e.TimeOffset)
		spanLimit := e.maxSpansPerTrace()
		spanCount := 0
		_, rootErr := e.walkTrace(ctx, root, nil, spanStart, elapsed, overrides, scenarioNames, &stats, &spanCount, spanLimit, false, false)
		stats.Traces++
		if rootErr {
			stats.FailedTraces++
		}
		if spanCount >= spanLimit {
			stats.SpansBounded++
		}
		if e.MaxTraces > 0 && stats.Traces >= int64(e.MaxTraces) {
			e.finaliseStats(&stats, startTime)
			return &stats, nil
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

func waitZeroRate(ctx context.Context) bool {
	timer := time.NewTimer(zeroRateIdleInterval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
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

func (e *Engine) maxInFlightTraces() int {
	if e.MaxInFlightTraces > 0 {
		return e.MaxInFlightTraces
	}
	return DefaultMaxInFlightTraces
}

// runRealtime plans traces on the main goroutine and emits them in background
// goroutines at wall-clock times matching simulated timestamps.
//
// SimulationState (queue depth, circuit breakers, backpressure) is updated
// during planning, not emission. This means the state sees each trace as
// completing instantly rather than over its wall-clock duration. For a synthetic
// data generator this is an acceptable trade-off that keeps the state serial.
func (e *Engine) runRealtime(ctx context.Context) (*Stats, error) {
	var stats Stats
	startTime := time.Now()
	deadline := startTime.Add(e.Duration)

	var wg sync.WaitGroup
	sem := make(chan struct{}, e.maxInFlightTraces())

	var rstats realtimeStats
	var lastActive []Scenario

	intervalTimer := time.NewTimer(0)
	defer intervalTimer.Stop()
	<-intervalTimer.C

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			e.mergeRealtimeStats(&stats, &rstats)
			e.finaliseStats(&stats, startTime)
			return &stats, nil
		default:
		}

		now := time.Now()
		if now.After(deadline) {
			wg.Wait()
			e.mergeRealtimeStats(&stats, &rstats)
			e.finaliseStats(&stats, startTime)
			return &stats, nil
		}

		elapsed := now.Sub(startTime)

		var overrides map[string]Override
		var scenarioNames []string
		trafficPattern := e.Traffic
		if len(e.Scenarios) > 0 {
			active := ActiveScenarios(e.Scenarios, elapsed)
			if len(active) > 0 {
				overrides = ResolveOverrides(active)
				if tp := ResolveTraffic(active); tp != nil {
					trafficPattern = tp
				}
				if e.LabelScenarios {
					scenarioNames = make([]string, len(active))
					for i, s := range active {
						scenarioNames[i] = s.Name
					}
				}
			}
			// Scenario contents are static, so the merged overrides only
			// change when the active set does — notify observers on
			// transitions rather than every iteration.
			if !activeScenariosEqual(active, lastActive) {
				notifyOverrides(e.Observers, overrides)
				lastActive = active
			}
		}

		rate := trafficPattern.Rate(elapsed)
		if rate <= 0 {
			if waitZeroRate(ctx) {
				wg.Wait()
				e.mergeRealtimeStats(&stats, &rstats)
				e.finaliseStats(&stats, startTime)
				return &stats, nil
			}
			continue
		}

		// Block until a slot is available (semaphore).
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			e.mergeRealtimeStats(&stats, &rstats)
			e.finaliseStats(&stats, startTime)
			return &stats, nil
		}

		root := e.Topology.Roots[e.Rng.IntN(len(e.Topology.Roots))]

		spanStart := now
		spanLimit := e.maxSpansPerTrace()
		spanCount := 0

		// planTrace does not count Spans or Errors — those are counted
		// atomically during emission. It does count Timeouts, Retries,
		// QueueRejections, and CircuitBreakerTrips which are plan-phase
		// decisions.
		var plans []SpanPlan
		_, rootErr := e.planTrace(root, nil, -1, spanStart, elapsed, overrides, scenarioNames, &stats, &plans, &spanCount, spanLimit, false, false)
		stats.Traces++
		if rootErr {
			stats.FailedTraces++
		}
		if spanCount >= spanLimit {
			stats.SpansBounded++
		}
		wg.Go(func() {
			defer func() { <-sem }()
			emitTrace(ctx, plans, spanStart, now, e.Tracers, e.Observers, &rstats, e.linkRegistry)
		})

		if e.MaxTraces > 0 && stats.Traces >= int64(e.MaxTraces) {
			wg.Wait()
			e.mergeRealtimeStats(&stats, &rstats)
			e.finaliseStats(&stats, startTime)
			return &stats, nil
		}

		interval := time.Duration(float64(time.Second) / rate)
		intervalTimer.Reset(interval)
		select {
		case <-ctx.Done():
			wg.Wait()
			e.mergeRealtimeStats(&stats, &rstats)
			e.finaliseStats(&stats, startTime)
			return &stats, nil
		case <-intervalTimer.C:
		}
	}
}

func (e *Engine) mergeRealtimeStats(stats *Stats, rstats *realtimeStats) {
	stats.Spans += rstats.Spans.Load()
	stats.Errors += rstats.Errors.Load()
}

func (e *Engine) maxSpansPerTrace() int {
	if e.MaxSpansPerTrace > 0 {
		return e.MaxSpansPerTrace
	}
	return DefaultMaxSpansPerTrace
}

// walkTrace recursively generates spans for an operation and its downstream calls.
// Returns the span end time and whether the span errored (own error rate or cascaded from children).
// parent is the calling operation, nil for roots; it is reported to observers.
// spanCount tracks the number of spans generated in this trace; no new spans are created once it reaches spanLimit.
// elapsed is the simulation wall-clock time since engine start, used for state tracking.
// isAsync indicates the span was invoked via an async call and should use CONSUMER span kind.
// isProducer indicates the span was invoked via a producer call and should use PRODUCER span kind.
func (e *Engine) walkTrace(ctx context.Context, op, parent *Operation, startTime time.Time, elapsed time.Duration, overrides map[string]Override, scenarioNames []string, stats *Stats, spanCount *int, spanLimit int, isAsync, isProducer bool) (time.Time, bool) {
	if *spanCount >= spanLimit {
		return startTime, false
	}
	*spanCount++
	tracer := e.Tracers(op.Service.Name)

	// Determine effective duration, error rate, and attributes (apply overrides if active)
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

	// Consult simulation state for queue depth, circuit breaker, backpressure
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
			return e.emitRejectionSpan(ctx, op, parent, startTime, reason, scenarioNames, stats, isAsync, isProducer)
		}
		if durationMult > 1.0 {
			duration.Mean = time.Duration(float64(duration.Mean) * durationMult)
		}
		errorRate = min(errorRate+errAdd, 1.0)
		opState.Enter()
	}

	// Determine span kind: SERVER for roots, PRODUCER for producer callees,
	// CONSUMER for async callees, INTERNAL for same-service sync callees,
	// CLIENT otherwise.
	kind := spanKindFor(e.Topology, op, parent, isAsync, isProducer)

	startAttrs := []attribute.KeyValue{
		attribute.String("synth.service", op.Service.Name),
		attribute.String("synth.operation", op.Name),
	}
	if e.LabelScenarios {
		startAttrs = append(startAttrs, attribute.StringSlice("synth.scenarios", scenarioNames))
	}

	startOpts := []trace.SpanStartOption{
		trace.WithTimestamp(startTime),
		trace.WithSpanKind(kind),
		trace.WithAttributes(startAttrs...),
	}
	if len(op.Links) > 0 && e.linkRegistry != nil {
		var links []trace.Link
		for _, linked := range op.Links {
			if sc, ok := e.linkRegistry.load(linked.Operation.Ref); ok {
				links = append(links, trace.Link{
					SpanContext: sc,
					Attributes:  attributeKeyValues(linked.Attributes, e.Rng),
				})
			}
		}
		if len(links) > 0 {
			startOpts = append(startOpts, trace.WithLinks(links...))
		}
	}

	ctx, span := tracer.Start(ctx, op.Name, startOpts...)

	if e.linkRegistry != nil {
		e.linkRegistry.store(op.Ref, span.SpanContext())
	}

	notifySpanStart(e.Observers, op.Service.Name, op.Name)

	// Collect attributes for both the span and observers
	spanAttrs := make([]attribute.KeyValue, 0, len(op.Service.Attributes)+len(opAttrs))
	for k, v := range op.Service.Attributes {
		spanAttrs = append(spanAttrs, attribute.String(k, v))
	}
	for _, a := range opAttrs {
		spanAttrs = append(spanAttrs, typedAttribute(a.Key, a.Gen.Generate(e.Rng)))
	}
	span.SetAttributes(spanAttrs...)

	for _, evt := range op.Events {
		evtOpts := []trace.EventOption{
			trace.WithTimestamp(startTime.Add(evt.Delay)),
		}
		if len(evt.Attributes) > 0 {
			evtAttrs := make([]attribute.KeyValue, 0, len(evt.Attributes))
			for _, a := range evt.Attributes {
				evtAttrs = append(evtAttrs, typedAttribute(a.Key, a.Gen.Generate(e.Rng)))
			}
			evtOpts = append(evtOpts, trace.WithAttributes(evtAttrs...))
		}
		span.AddEvent(evt.Name, evtOpts...)
	}

	ownError := false
	if errorRate > 0 {
		if forced, ok := e.forcedChoice(choiceKindOperationError, op.Ref, "", -1); ok {
			ownError = forced
		} else {
			ownError = e.Rng.Float64() < errorRate
		}
	}

	// Sample own processing duration
	ownDuration := duration.Sample(e.Rng)

	// Pre-call work: half the own duration before calling downstream
	preCallDuration := ownDuration / 2
	childStartTime := startTime.Add(preCallDuration)

	// Build effective call list (base calls + scenario adds - removes)
	baseCalls := effectiveCalls(op, overrides)

	// Filter calls by condition and probability (uses own error state, not cascaded)
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

	// Walk downstream calls (parallel or sequential) with fan-out
	latestChildEnd := childStartTime
	anyChildFailed := false
	if op.CallStyle == "sequential" {
		nextStart := childStartTime
		for _, active := range activeCalls {
			count := max(active.Call.Count, 1)
			for range count {
				perceivedEnd, failed := e.executeCall(ctx, active, op, nextStart, elapsed, overrides, scenarioNames, stats, spanCount, spanLimit)
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
				perceivedEnd, failed := e.executeCall(ctx, active, op, childStartTime, elapsed, overrides, scenarioNames, stats, spanCount, spanLimit)
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
		parentService, parentOperation := parentNames(parent)
		info := newSpanInfo(
			op.Service.Name, op.Name,
			parentService, parentOperation,
			startTime, endTime.Sub(startTime),
			isError, kind,
			attrsCopy, scenarioNames,
			span.SpanContext(),
		)
		for _, obs := range e.Observers {
			obs.Observe(info)
		}
	}

	return endTime, isError
}

// parentNames returns the service and operation names of a parent operation,
// or empty strings when parent is nil (root spans).
func parentNames(parent *Operation) (string, string) {
	if parent == nil {
		return "", ""
	}
	return parent.Service.Name, parent.Name
}

// emitRejectionSpan creates a short error span for a rejected request.
// The caller (walkTrace) has already counted this span against the trace's
// span limit, so spanCount is not incremented here.
func (e *Engine) emitRejectionSpan(ctx context.Context, op, parent *Operation, startTime time.Time, reason string, scenarioNames []string, stats *Stats, isAsync, isProducer bool) (time.Time, bool) {
	tracer := e.Tracers(op.Service.Name)
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

	_, span := tracer.Start(ctx, op.Name,
		trace.WithTimestamp(startTime),
		trace.WithSpanKind(kind),
		trace.WithAttributes(rejAttrs...),
	)
	span.SetStatus(codes.Error, reason)
	span.RecordError(fmt.Errorf("rejected: %s", reason), trace.WithTimestamp(endTime))
	span.End(trace.WithTimestamp(endTime))

	stats.Spans++
	stats.Errors++

	if len(e.Observers) > 0 {
		notifySpanStart(e.Observers, op.Service.Name, op.Name)
		parentService, parentOperation := parentNames(parent)
		info := newSpanInfo(
			op.Service.Name, op.Name,
			parentService, parentOperation,
			startTime, rejectionDuration,
			true, kind,
			[]attribute.KeyValue{
				attribute.Bool("synth.rejected", true),
				attribute.String("synth.rejection_reason", reason),
			},
			scenarioNames,
			span.SpanContext(),
		)
		for _, obs := range e.Observers {
			obs.Observe(info)
		}
	}

	return endTime, true
}

type activeCall struct {
	Call        Call
	ChoiceIndex int
}

// executeCall runs a single downstream call, applying timeout capping and retries.
// parent is the calling operation.
func (e *Engine) executeCall(ctx context.Context, active activeCall, parent *Operation, callStart time.Time, elapsed time.Duration, overrides map[string]Override, scenarioNames []string, stats *Stats, spanCount *int, spanLimit int) (time.Time, bool) {
	call := active.Call
	maxAttempts := 1 + call.Retries
	attemptStart := callStart

	for attempt := range maxAttempts {
		childEnd, childErr := e.walkTrace(ctx, call.Operation, parent, attemptStart, elapsed, overrides, scenarioNames, stats, spanCount, spanLimit, call.Async, call.Producer)
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

	return callStart, true // unreachable: loop always returns on final iteration
}

// activeScenariosEqual reports whether two active scenario sets are the same.
// Used to skip redundant observer notifications between activation transitions.
func activeScenariosEqual(a, b []Scenario) bool {
	return slices.EqualFunc(a, b, func(x, y Scenario) bool {
		return x.Name == y.Name && x.Start == y.Start && x.End == y.End && x.Priority == y.Priority
	})
}

// isRoot checks whether an operation is a root (entry point) in the topology.
func isRoot(topo *Topology, op *Operation) bool {
	return slices.Contains(topo.Roots, op)
}

// spanKindFor determines the OTel span kind for an operation given how it was
// invoked: SERVER for trace roots, PRODUCER for the callee of a producer call
// (an async enqueue/publish step), CONSUMER for the callee of an async call,
// INTERNAL for a sync callee on the same service as its caller (an in-process
// sub-operation with no remote hop), and CLIENT for cross-service sync calls.
// Roots always win; producer takes precedence over async.
func spanKindFor(topo *Topology, op, parent *Operation, isAsync, isProducer bool) trace.SpanKind {
	switch {
	case isRoot(topo, op):
		return trace.SpanKindServer
	case isProducer:
		return trace.SpanKindProducer
	case isAsync:
		return trace.SpanKindConsumer
	case parent != nil && parent.Service.Name == op.Service.Name:
		return trace.SpanKindInternal
	default:
		return trace.SpanKindClient
	}
}

// effectiveCalls returns the call list for an operation, applying scenario add/remove overrides.
// Returns the base call list directly when no call changes are active (zero allocation fast path).
func effectiveCalls(op *Operation, overrides map[string]Override) []Call {
	if len(overrides) == 0 {
		return op.Calls
	}
	ov, ok := overrides[op.Ref]
	if !ok || !ov.HasCallChanges() {
		return op.Calls
	}

	calls := make([]Call, 0, len(op.Calls)+len(ov.AddCalls))

	for _, c := range op.Calls {
		if !ov.RemoveCalls[c.Operation.Ref] {
			calls = append(calls, c)
		}
	}

	calls = append(calls, ov.AddCalls...)
	return calls
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

func attributeKeyValues(attrs Attributes, rng *rand.Rand) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	kvs := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		kvs = append(kvs, typedAttribute(a.Key, a.Gen.Generate(rng)))
	}
	return kvs
}
