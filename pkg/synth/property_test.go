// Property-based tests for the synth engine using pgregory.net/rapid
// Covers topology conformance, span nesting, error cascading, stats consistency,
// and scenario activation/override resolution
package synth

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"pgregory.net/rapid"

	"github.com/stretchr/testify/assert"
)

// --- Generators ---

// simpleConfig is a rapid.Custom generator for valid Config values.
var simpleConfig = rapid.Custom(genSimpleConfig)

// genDurationString generates a valid duration string using regex matching.
var genDurationString = rapid.StringMatching(`[1-9][0-9]{0,2}ms`)

// genRateString generates a valid rate string using regex matching.
var genRateString = rapid.StringMatching(`[1-9][0-9]{0,3}/[smh]`)

// genErrorRateString generates a valid error rate percentage.
var genErrorRateString = rapid.StringMatching(`[1-9][0-9]?%`)

// genSimpleConfig generates a valid Config with 1-4 services, each with 1-3 operations,
// and a DAG of calls between them (no cycles).
func genSimpleConfig(t *rapid.T) *Config {
	type svcOp struct{ svc, op string }

	nSvcs := rapid.IntRange(1, 4).Draw(t, "nSvcs")
	svcNames := make([]string, nSvcs)
	for i := range nSvcs {
		svcNames[i] = fmt.Sprintf("svc%d", i)
	}

	var allOps []svcOp
	svcs := make([]ServiceConfig, nSvcs)
	for i, svcName := range svcNames {
		nOps := rapid.IntRange(1, 3).Draw(t, fmt.Sprintf("nOps%d", i))
		ops := make([]OperationConfig, nOps)
		for j := range nOps {
			opName := fmt.Sprintf("op%d", j)
			dur := genDurationString.Draw(t, fmt.Sprintf("dur%d_%d", i, j))
			hasErr := rapid.Bool().Draw(t, fmt.Sprintf("hasErr%d_%d", i, j))
			ops[j] = OperationConfig{
				Name:     opName,
				Duration: dur,
			}
			if hasErr {
				ops[j].ErrorRate = genErrorRateString.Draw(t, fmt.Sprintf("errRate%d_%d", i, j))
			}
			allOps = append(allOps, svcOp{svcName, opName})
		}
		svcs[i] = ServiceConfig{Name: svcName, Operations: ops}
	}

	// Add calls: each operation can call operations from later services only (ensures DAG)
	for i := range svcs {
		for j := range svcs[i].Operations {
			// Only call operations in services with higher index
			var targets []svcOp
			for _, so := range allOps {
				for k, sn := range svcNames {
					if k > i && sn == so.svc {
						targets = append(targets, so)
					}
				}
			}
			if len(targets) == 0 {
				continue
			}
			nCalls := rapid.IntRange(0, min(len(targets), 2)).Draw(t, fmt.Sprintf("nCalls%d_%d", i, j))
			if nCalls == 0 {
				continue
			}
			// Shuffle targets and pick first nCalls
			shuffled := rapid.Permutation(targets).Draw(t, fmt.Sprintf("perm%d_%d", i, j))
			calls := make([]CallConfig, nCalls)
			for c := range nCalls {
				calls[c] = CallConfig{Target: shuffled[c].svc + "." + shuffled[c].op}
			}
			svcs[i].Operations[j].Calls = calls
		}
	}

	return &Config{
		Services: svcs,
		Traffic:  TrafficConfig{Rate: genRateString.Draw(t, "rate")},
	}
}

// genSeed draws a random seed for the engine's RNG.
func genSeed(t *rapid.T) uint64 {
	return rapid.Uint64().Draw(t, "seed")
}

// walkOnce builds a topology from config, walks one trace, and returns the exporter's spans.
func walkOnce(t *rapid.T, cfg *Config) (*Topology, []tracetest.SpanStub, *Stats) {
	topo, err := BuildTopology(cfg)
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	if len(topo.Roots) == 0 {
		t.Skip("no root operations")
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	seed := genSeed(t)
	rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

	engine := &Engine{
		Topology: topo,
		Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:      rng,
	}

	rootOp := topo.Roots[rng.IntN(len(topo.Roots))]
	now := time.Now()
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace)

	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}

	return topo, exporter.GetSpans(), &stats
}

// --- Topology conformance ---

func TestProperty_Engine_SpansMatchTopology(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, spans, _ := walkOnce(t, cfg)

		knownOps := make(map[string]bool)
		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				knownOps[op.Ref] = true
			}
		}

		for _, span := range spans {
			svcName := ""
			opName := span.Name
			for _, attr := range span.Attributes {
				if string(attr.Key) == "synth.service" {
					svcName = attr.Value.AsString()
				}
			}
			ref := svcName + "." + opName
			if !knownOps[ref] {
				t.Fatalf("span %q not in topology (known: %v)", ref, knownOps)
			}
		}
	})
}

// --- Call graph correctness ---

func TestProperty_Engine_CallGraphMatchesTopology(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, spans, _ := walkOnce(t, cfg)

		// Build a set of valid caller→callee pairs from topology
		validCalls := make(map[string]map[string]bool) // parentRef -> set of childRefs
		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				targets := make(map[string]bool)
				for _, call := range op.Calls {
					targets[call.Operation.Ref] = true
				}
				validCalls[op.Ref] = targets
			}
		}

		// Index spans by span ID
		spanByID := make(map[trace.SpanID]tracetest.SpanStub)
		for _, s := range spans {
			spanByID[s.SpanContext.SpanID()] = s
		}

		refOf := func(s tracetest.SpanStub) string {
			svcName := ""
			for _, attr := range s.Attributes {
				if string(attr.Key) == "synth.service" {
					svcName = attr.Value.AsString()
				}
			}
			return svcName + "." + s.Name
		}

		for _, s := range spans {
			if !s.Parent.SpanID().IsValid() {
				continue // root span
			}
			parent, ok := spanByID[s.Parent.SpanID()]
			if !ok {
				continue // parent not in this batch (shouldn't happen but safe)
			}
			parentRef := refOf(parent)
			childRef := refOf(s)
			if !validCalls[parentRef][childRef] {
				t.Fatalf("call %s -> %s not in topology", parentRef, childRef)
			}
		}
	})
}

// --- Span nesting ---

func TestProperty_Engine_ChildSpansNestedInParent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		spanByID := make(map[trace.SpanID]tracetest.SpanStub)
		for _, s := range spans {
			spanByID[s.SpanContext.SpanID()] = s
		}

		for _, child := range spans {
			if !child.Parent.SpanID().IsValid() {
				continue
			}
			parent, ok := spanByID[child.Parent.SpanID()]
			if !ok {
				continue
			}
			if child.StartTime.Before(parent.StartTime) {
				t.Fatalf("child %s starts before parent %s", child.Name, parent.Name)
			}
			if parent.EndTime.Before(child.EndTime) {
				t.Fatalf("parent %s (end=%v) ends before child %s (end=%v)",
					parent.Name, parent.EndTime, child.Name, child.EndTime)
			}
		}
	})
}

// --- Positive duration ---

func TestProperty_Engine_SpanDurationsPositive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		for _, s := range spans {
			dur := s.EndTime.Sub(s.StartTime)
			if dur < 0 {
				t.Fatalf("span %s has negative duration %v", s.Name, dur)
			}
		}
	})
}

// --- Stats consistency ---

func TestProperty_Engine_StatsMatchSpans(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, stats := walkOnce(t, cfg)

		if int64(len(spans)) != stats.Spans {
			t.Fatalf("exporter has %d spans, stats says %d", len(spans), stats.Spans)
		}
	})
}

func TestProperty_Engine_ErrorsBounded(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, _, stats := walkOnce(t, cfg)

		if stats.Errors > stats.Spans {
			t.Fatalf("errors %d > spans %d", stats.Errors, stats.Spans)
		}
		if stats.Errors < 0 {
			t.Fatalf("errors %d is negative", stats.Errors)
		}
	})
}

// --- Error cascading ---

func TestProperty_Engine_ErrorCascadesToParent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		spanByID := make(map[trace.SpanID]tracetest.SpanStub)
		for _, s := range spans {
			spanByID[s.SpanContext.SpanID()] = s
		}

		// Build children map
		childrenOf := make(map[trace.SpanID][]tracetest.SpanStub)
		for _, s := range spans {
			if s.Parent.SpanID().IsValid() {
				childrenOf[s.Parent.SpanID()] = append(childrenOf[s.Parent.SpanID()], s)
			}
		}

		for _, parent := range spans {
			children := childrenOf[parent.SpanContext.SpanID()]
			if len(children) == 0 {
				continue
			}
			anyChildErrored := false
			for _, child := range children {
				if child.Status.Code == codes.Error {
					anyChildErrored = true
					break
				}
			}
			if anyChildErrored && parent.Status.Code != codes.Error {
				t.Fatalf("child of %s errored but parent did not cascade", parent.Name)
			}
		}
	})
}

// --- Root span kind ---

func TestProperty_Engine_RootSpanIsServer(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		for _, s := range spans {
			if !s.Parent.SpanID().IsValid() {
				if s.SpanKind != trace.SpanKindServer {
					t.Fatalf("root span %s has kind %v, expected Server", s.Name, s.SpanKind)
				}
			}
		}
	})
}

func TestProperty_Engine_NonRootSpanIsClient(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		for _, s := range spans {
			if s.Parent.SpanID().IsValid() {
				if s.SpanKind != trace.SpanKindClient {
					t.Fatalf("non-root span %s has kind %v, expected Client", s.Name, s.SpanKind)
				}
			}
		}
	})
}

// --- Single trace ---

func TestProperty_Engine_AllSpansShareTraceID(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		if len(spans) == 0 {
			return
		}
		traceID := spans[0].SpanContext.TraceID()
		for _, s := range spans[1:] {
			if s.SpanContext.TraceID() != traceID {
				t.Fatalf("span %s has trace ID %v, expected %v", s.Name, s.SpanContext.TraceID(), traceID)
			}
		}
	})
}

// --- Scenario generators ---

// genScenario generates a random Scenario with a valid activation window.
func genScenario(t *rapid.T, label string, refs []string) Scenario {
	startMin := rapid.Int64Range(0, int64(10*time.Minute)).Draw(t, label+"-start")
	dur := rapid.Int64Range(int64(time.Second), int64(5*time.Minute)).Draw(t, label+"-dur")
	priority := rapid.IntRange(0, 100).Draw(t, label+"-priority")

	overrides := make(map[string]Override)
	for _, ref := range refs {
		if !rapid.Bool().Draw(t, label+"-has-"+ref) {
			continue
		}
		var ov Override
		if rapid.Bool().Draw(t, label+"-hasDur-"+ref) {
			meanMs := rapid.IntRange(1, 1000).Draw(t, label+"-durMs-"+ref)
			ov.Duration = Distribution{Mean: time.Duration(meanMs) * time.Millisecond}
		}
		if rapid.Bool().Draw(t, label+"-hasErr-"+ref) {
			errPct := rapid.Float64Range(0, 1.0).Draw(t, label+"-errRate-"+ref)
			ov.ErrorRate = errPct
			ov.HasErrorRate = true
		}
		overrides[ref] = ov
	}

	start := time.Duration(startMin)
	return Scenario{
		Name:      label,
		Start:     start,
		End:       start + time.Duration(dur),
		Priority:  priority,
		Overrides: overrides,
	}
}

// genScenarioList generates 1-5 scenarios with overlapping windows.
func genScenarioList(t *rapid.T, refs []string) []Scenario {
	n := rapid.IntRange(1, 5).Draw(t, "nScenarios")
	scenarios := make([]Scenario, n)
	for i := range n {
		scenarios[i] = genScenario(t, fmt.Sprintf("sc%d", i), refs)
	}
	return scenarios
}

// --- ActiveScenarios properties ---

func TestProperty_ActiveScenarios_WindowCorrectness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		refs := []string{"svc.op"}
		scenarios := genScenarioList(t, refs)
		elapsed := time.Duration(rapid.Int64Range(0, int64(15*time.Minute)).Draw(t, "elapsed"))

		active := ActiveScenarios(scenarios, elapsed)

		for _, sc := range active {
			if elapsed < sc.Start || elapsed >= sc.End {
				t.Fatalf("scenario %q active at %v but window is [%v, %v)", sc.Name, elapsed, sc.Start, sc.End)
			}
		}

		// Verify nothing was missed
		activeNames := make(map[string]bool)
		for _, sc := range active {
			activeNames[sc.Name] = true
		}
		for _, sc := range scenarios {
			shouldBeActive := elapsed >= sc.Start && elapsed < sc.End
			if shouldBeActive && !activeNames[sc.Name] {
				t.Fatalf("scenario %q should be active at %v (window [%v, %v)) but was not returned",
					sc.Name, elapsed, sc.Start, sc.End)
			}
		}
	})
}

func TestProperty_ActiveScenarios_SortedByPriority(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		refs := []string{"svc.op"}
		scenarios := genScenarioList(t, refs)
		elapsed := time.Duration(rapid.Int64Range(0, int64(15*time.Minute)).Draw(t, "elapsed"))

		active := ActiveScenarios(scenarios, elapsed)

		for i := 1; i < len(active); i++ {
			if active[i].Priority < active[i-1].Priority {
				t.Fatalf("active scenarios not sorted by priority: %d at index %d, %d at index %d",
					active[i-1].Priority, i-1, active[i].Priority, i)
			}
		}
	})
}

func TestProperty_ActiveScenarios_StableOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		refs := []string{"svc.op"}
		scenarios := genScenarioList(t, refs)
		elapsed := time.Duration(rapid.Int64Range(0, int64(15*time.Minute)).Draw(t, "elapsed"))

		active1 := ActiveScenarios(scenarios, elapsed)
		active2 := ActiveScenarios(scenarios, elapsed)

		if len(active1) != len(active2) {
			t.Fatalf("ActiveScenarios not deterministic: got %d then %d", len(active1), len(active2))
		}
		for i := range active1 {
			if active1[i].Name != active2[i].Name {
				t.Fatalf("ActiveScenarios not stable: index %d was %q then %q", i, active1[i].Name, active2[i].Name)
			}
		}
	})
}

// --- ResolveOverrides properties ---

func TestProperty_ResolveOverrides_LastWins(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ref := "svc.op"
		dur1 := rapid.IntRange(1, 500).Draw(t, "dur1")
		dur2 := rapid.IntRange(501, 1000).Draw(t, "dur2")

		scenarios := []Scenario{
			{
				Priority: 1,
				Overrides: map[string]Override{
					ref: {Duration: Distribution{Mean: time.Duration(dur1) * time.Millisecond}},
				},
			},
			{
				Priority: 2,
				Overrides: map[string]Override{
					ref: {Duration: Distribution{Mean: time.Duration(dur2) * time.Millisecond}},
				},
			},
		}

		overrides := ResolveOverrides(scenarios)
		got := overrides[ref].Duration.Mean
		want := time.Duration(dur2) * time.Millisecond
		if got != want {
			t.Fatalf("expected last-wins duration %v, got %v", want, got)
		}
	})
}

func TestProperty_ResolveOverrides_PartialPreservesEarlier(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ref := "svc.op"
		durMs := rapid.IntRange(1, 1000).Draw(t, "durMs")
		errRate := rapid.Float64Range(0.01, 1.0).Draw(t, "errRate")

		scenarios := []Scenario{
			{
				Overrides: map[string]Override{
					ref: {
						Duration:     Distribution{Mean: time.Duration(100) * time.Millisecond},
						ErrorRate:    errRate,
						HasErrorRate: true,
					},
				},
			},
			{
				Overrides: map[string]Override{
					ref: {Duration: Distribution{Mean: time.Duration(durMs) * time.Millisecond}},
				},
			},
		}

		overrides := ResolveOverrides(scenarios)
		ov := overrides[ref]

		if ov.Duration.Mean != time.Duration(durMs)*time.Millisecond {
			t.Fatalf("duration should be %dms, got %v", durMs, ov.Duration.Mean)
		}
		if !ov.HasErrorRate {
			t.Fatal("error rate should be preserved from first scenario")
		}
		if ov.ErrorRate != errRate {
			t.Fatalf("error rate should be %f, got %f", errRate, ov.ErrorRate)
		}
	})
}

func TestProperty_ResolveOverrides_DoesNotMutateInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ref := "svc.op"
		nScenarios := rapid.IntRange(2, 4).Draw(t, "nScenarios")
		scenarios := make([]Scenario, nScenarios)

		originalAttrCounts := make([]int, nScenarios)
		originalAddCallCounts := make([]int, nScenarios)

		for i := range nScenarios {
			attrs := make(map[string]AttributeGenerator)
			attrKey := fmt.Sprintf("attr%d", i)
			attrs[attrKey] = &StaticValue{Value: fmt.Sprintf("val%d", i)}

			scenarios[i] = Scenario{
				Overrides: map[string]Override{
					ref: {
						Attributes: attrs,
						AddCalls:   []Call{{Operation: &Operation{Ref: fmt.Sprintf("target%d.op", i)}}},
					},
				},
			}
			originalAttrCounts[i] = len(attrs)
			originalAddCallCounts[i] = 1
		}

		_ = ResolveOverrides(scenarios)

		for i := range nScenarios {
			ov := scenarios[i].Overrides[ref]
			if len(ov.Attributes) != originalAttrCounts[i] {
				t.Fatalf("scenario %d attributes mutated: was %d, now %d",
					i, originalAttrCounts[i], len(ov.Attributes))
			}
			if len(ov.AddCalls) != originalAddCallCounts[i] {
				t.Fatalf("scenario %d AddCalls mutated: was %d, now %d",
					i, originalAddCallCounts[i], len(ov.AddCalls))
			}
		}
	})
}

func TestProperty_ResolveOverrides_AllRefsPresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		refs := []string{"svc.op", "svc.op2", "svc2.op"}
		scenarios := genScenarioList(t, refs)

		overrides := ResolveOverrides(scenarios)

		expectedRefs := make(map[string]bool)
		for _, sc := range scenarios {
			for ref := range sc.Overrides {
				expectedRefs[ref] = true
			}
		}
		for ref := range expectedRefs {
			if _, ok := overrides[ref]; !ok {
				t.Fatalf("ref %q present in scenarios but missing from merged overrides", ref)
			}
		}
	})
}

// --- ResolveTraffic properties ---

func TestProperty_ResolveTraffic_HighestPriorityWins(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "nScenarios")
		scenarios := make([]Scenario, n)

		lastTrafficIdx := -1
		rates := make([]float64, n)

		for i := range n {
			hasTraffic := rapid.Bool().Draw(t, fmt.Sprintf("hasTraffic%d", i))
			if hasTraffic {
				rate := rapid.IntRange(1, 1000).Draw(t, fmt.Sprintf("rate%d", i))
				rateStr := fmt.Sprintf("%d/s", rate)
				pattern, err := NewTrafficPattern(TrafficConfig{Rate: rateStr})
				if err != nil {
					t.Fatalf("NewTrafficPattern(%q): %v", rateStr, err)
				}
				scenarios[i] = Scenario{Priority: i, Traffic: pattern}
				rates[i] = float64(rate)
				lastTrafficIdx = i
			} else {
				scenarios[i] = Scenario{Priority: i}
			}
		}

		result := ResolveTraffic(scenarios)

		if lastTrafficIdx == -1 {
			if result != nil {
				t.Fatal("expected nil traffic when no scenario has traffic")
			}
		} else {
			if result == nil {
				t.Fatal("expected non-nil traffic")
			}
			gotRate := result.Rate(0)
			assert.InDelta(t, rates[lastTrafficIdx], gotRate, 0.1,
				"traffic should come from last scenario with traffic override")
		}
	})
}

// --- Engine with scenario overrides ---

func TestProperty_Engine_ScenarioOverrideApplied(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		durMs := rapid.IntRange(1, 50).Draw(t, "baseDur")
		overrideDurMs := rapid.IntRange(100, 500).Draw(t, "overrideDur")

		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: fmt.Sprintf("%dms", durMs),
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}

		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}

		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		overrides := map[string]Override{
			"svc.op": {Duration: Distribution{Mean: time.Duration(overrideDurMs) * time.Millisecond}},
		}

		engine := &Engine{
			Topology: topo,
			Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
			Rng:      rng,
		}

		var stats Stats
		engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, overrides, nil, &stats, new(int), DefaultMaxSpansPerTrace)

		if err := tp.ForceFlush(context.Background()); err != nil {
			t.Fatalf("ForceFlush: %v", err)
		}

		spans := exporter.GetSpans()
		if len(spans) != 1 {
			t.Fatalf("expected 1 span, got %d", len(spans))
		}

		spanDur := spans[0].EndTime.Sub(spans[0].StartTime)
		overrideDur := time.Duration(overrideDurMs) * time.Millisecond
		baseDur := time.Duration(durMs) * time.Millisecond

		if spanDur != overrideDur {
			t.Fatalf("span duration %v should equal override %v (base was %v)", spanDur, overrideDur, baseDur)
		}
	})
}

func TestProperty_Engine_ScenarioErrorRateOverride(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: "10ms",
				}},
			}},
			Traffic: TrafficConfig{Rate: "100/s"},
		}

		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}

		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		overrides := map[string]Override{
			"svc.op": {ErrorRate: 1.0, HasErrorRate: true},
		}

		engine := &Engine{
			Topology: topo,
			Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
			Rng:      rng,
		}

		var stats Stats
		engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, overrides, nil, &stats, new(int), DefaultMaxSpansPerTrace)

		if err := tp.ForceFlush(context.Background()); err != nil {
			t.Fatalf("ForceFlush: %v", err)
		}

		spans := exporter.GetSpans()
		if len(spans) != 1 {
			t.Fatalf("expected 1 span, got %d", len(spans))
		}

		if spans[0].Status.Code != codes.Error {
			t.Fatal("100% error rate override should produce error span")
		}
	})
}

// --- Attribute generators ---

func TestProperty_StaticValue_AlwaysReturnsSameValue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		val := rapid.String().Draw(t, "val")
		gen := &StaticValue{Value: val}
		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		for range 100 {
			got := gen.Generate(rng)
			if got != val {
				t.Fatalf("StaticValue returned %v, expected %v", got, val)
			}
		}
	})
}

func TestProperty_WeightedChoice_OutputInChoices(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "nChoices")
		values := make(map[string]int)
		for i := range n {
			key := fmt.Sprintf("choice%d", i)
			weight := rapid.IntRange(1, 100).Draw(t, fmt.Sprintf("w%d", i))
			values[key] = weight
		}

		gen, err := newWeightedChoice(values)
		if err != nil {
			t.Fatalf("newWeightedChoice: %v", err)
		}

		validChoices := make(map[any]bool)
		for k := range values {
			validChoices[k] = true
		}

		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		for range 200 {
			got := gen.Generate(rng)
			if !validChoices[got] {
				t.Fatalf("WeightedChoice returned %v, not in choices %v", got, validChoices)
			}
		}
	})
}

func TestProperty_BoolValue_OutputIsBoolean(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prob := rapid.Float64Range(0, 1).Draw(t, "prob")
		gen := &BoolValue{Probability: prob}
		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		for range 100 {
			got := gen.Generate(rng)
			if _, ok := got.(bool); !ok {
				t.Fatalf("BoolValue returned %T, expected bool", got)
			}
		}
	})
}

func TestProperty_BoolValue_ExtremesProbabilities(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := genSeed(t)

		// Probability 0 should always return false
		rng0 := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security
		gen0 := &BoolValue{Probability: 0}
		for range 100 {
			if gen0.Generate(rng0).(bool) {
				t.Fatal("BoolValue with probability 0 returned true")
			}
		}

		// Probability 1 should always return true
		rng1 := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security
		gen1 := &BoolValue{Probability: 1}
		for range 100 {
			if !gen1.Generate(rng1).(bool) {
				t.Fatal("BoolValue with probability 1 returned false")
			}
		}
	})
}

func TestProperty_RangeValue_WithinBounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lo := rapid.Int64Range(-1000, 1000).Draw(t, "lo")
		span := rapid.Int64Range(0, 2000).Draw(t, "span")
		hi := lo + span

		gen := &RangeValue{Min: lo, Max: hi}
		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		for range 200 {
			got := gen.Generate(rng).(int64)
			if got < lo || got > hi {
				t.Fatalf("RangeValue returned %d, outside [%d, %d]", got, lo, hi)
			}
		}
	})
}

func TestProperty_RangeValue_SingleValue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		val := rapid.Int64Range(-1000, 1000).Draw(t, "val")
		gen := &RangeValue{Min: val, Max: val}
		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		for range 50 {
			got := gen.Generate(rng).(int64)
			if got != val {
				t.Fatalf("RangeValue [%d,%d] returned %d", val, val, got)
			}
		}
	})
}

func TestProperty_SequenceValue_Monotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pattern := rapid.SampledFrom([]string{"req-{n}", "{n}", "id-{n}-end"}).Draw(t, "pattern")
		gen := &SequenceValue{Pattern: pattern}
		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		prev := ""
		for range 50 {
			got := gen.Generate(rng).(string)
			if got == prev {
				t.Fatalf("SequenceValue produced duplicate: %q", got)
			}
			prev = got
		}
	})
}

func TestProperty_NormalValue_MeanConverges(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		mean := rapid.Float64Range(-100, 100).Draw(t, "mean")
		stddev := rapid.Float64Range(0.1, 10).Draw(t, "stddev")
		gen := &NormalValue{Mean: mean, StdDev: stddev}
		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		sum := 0.0
		n := 10000
		for range n {
			sum += gen.Generate(rng).(float64)
		}
		sampleMean := sum / float64(n)

		tolerance := stddev * 0.1 // 10% of stddev
		if tolerance < 0.5 {
			tolerance = 0.5
		}
		diff := sampleMean - mean
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			t.Fatalf("NormalValue mean did not converge: expected ~%f, got %f (tolerance %f)",
				mean, sampleMean, tolerance)
		}
	})
}

// --- Distribution sampling ---

func TestProperty_Distribution_SampleNonNegative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		meanMs := rapid.IntRange(1, 1000).Draw(t, "meanMs")
		stddevMs := rapid.IntRange(0, 500).Draw(t, "stddevMs")
		dist := Distribution{
			Mean:   time.Duration(meanMs) * time.Millisecond,
			StdDev: time.Duration(stddevMs) * time.Millisecond,
		}

		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		for range 500 {
			d := dist.Sample(rng)
			if d < 0 {
				t.Fatalf("Distribution.Sample returned negative duration: %v", d)
			}
		}
	})
}

func TestProperty_Distribution_ZeroStdDevReturnsMean(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		meanMs := rapid.IntRange(1, 10000).Draw(t, "meanMs")
		dist := Distribution{Mean: time.Duration(meanMs) * time.Millisecond}

		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		for range 100 {
			d := dist.Sample(rng)
			if d != dist.Mean {
				t.Fatalf("zero stddev: expected %v, got %v", dist.Mean, d)
			}
		}
	})
}

func TestProperty_Distribution_MeanConverges(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		meanMs := rapid.IntRange(50, 500).Draw(t, "meanMs")
		stddevMs := rapid.IntRange(1, meanMs/5).Draw(t, "stddevMs")
		dist := Distribution{
			Mean:   time.Duration(meanMs) * time.Millisecond,
			StdDev: time.Duration(stddevMs) * time.Millisecond,
		}

		seed := genSeed(t)
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		sum := 0.0
		n := 10000
		for range n {
			sum += float64(dist.Sample(rng))
		}
		sampleMean := sum / float64(n)
		expected := float64(dist.Mean)

		tolerance := float64(dist.StdDev) * 0.1
		if tolerance < float64(time.Millisecond) {
			tolerance = float64(time.Millisecond)
		}
		diff := sampleMean - expected
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			t.Fatalf("Distribution mean did not converge: expected ~%v, got %v (diff %v, tolerance %v)",
				dist.Mean, time.Duration(sampleMean), time.Duration(diff), time.Duration(tolerance))
		}
	})
}

func TestProperty_ParseDistribution_RoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		meanMs := rapid.IntRange(1, 10000).Draw(t, "meanMs")
		hasStdDev := rapid.Bool().Draw(t, "hasStdDev")

		var dist Distribution
		dist.Mean = time.Duration(meanMs) * time.Millisecond
		if hasStdDev {
			stddevMs := rapid.IntRange(1, 1000).Draw(t, "stddevMs")
			dist.StdDev = time.Duration(stddevMs) * time.Millisecond
		}

		s := dist.String()
		parsed, err := ParseDistribution(s)
		if err != nil {
			t.Fatalf("ParseDistribution(%q): %v", s, err)
		}
		if parsed.Mean != dist.Mean {
			t.Fatalf("round-trip mean: %v != %v (string was %q)", parsed.Mean, dist.Mean, s)
		}
		if parsed.StdDev != dist.StdDev {
			t.Fatalf("round-trip stddev: %v != %v (string was %q)", parsed.StdDev, dist.StdDev, s)
		}
	})
}

// --- Topology building ---

func TestProperty_BuildTopology_RefsCorrect(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}

		for svcName, svc := range topo.Services {
			if svc.Name != svcName {
				t.Fatalf("service map key %q != service.Name %q", svcName, svc.Name)
			}
			for opName, op := range svc.Operations {
				if op.Name != opName {
					t.Fatalf("operation map key %q != op.Name %q", opName, op.Name)
				}
				expectedRef := svcName + "." + opName
				if op.Ref != expectedRef {
					t.Fatalf("op.Ref %q != expected %q", op.Ref, expectedRef)
				}
				if op.Service != svc {
					t.Fatalf("op %q.Service pointer does not match parent service", op.Ref)
				}
			}
		}
	})
}

func TestProperty_BuildTopology_NoCycles(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}

		// DFS to verify no cycles
		const (
			unvisited = iota
			visiting
			visited
		)
		state := make(map[*Operation]int)
		var visit func(op *Operation) error
		visit = func(op *Operation) error {
			if state[op] == visiting {
				return fmt.Errorf("cycle at %s", op.Ref)
			}
			if state[op] == visited {
				return nil
			}
			state[op] = visiting
			for _, call := range op.Calls {
				if err := visit(call.Operation); err != nil {
					return err
				}
			}
			state[op] = visited
			return nil
		}
		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				if err := visit(op); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
}

func TestProperty_BuildTopology_RootsNotCalledByAnyone(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}

		called := make(map[*Operation]bool)
		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				for _, call := range op.Calls {
					called[call.Operation] = true
				}
			}
		}

		for _, root := range topo.Roots {
			if called[root] {
				t.Fatalf("root %s is called by another operation", root.Ref)
			}
		}
	})
}

func TestProperty_BuildTopology_RootsComplete(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}

		called := make(map[*Operation]bool)
		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				for _, call := range op.Calls {
					called[call.Operation] = true
				}
			}
		}

		rootSet := make(map[*Operation]bool)
		for _, root := range topo.Roots {
			rootSet[root] = true
		}

		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				if !called[op] && !rootSet[op] {
					t.Fatalf("operation %s is not called and not in roots", op.Ref)
				}
			}
		}
	})
}

func TestProperty_BuildTopology_RootsSorted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}

		for i := 1; i < len(topo.Roots); i++ {
			a, b := topo.Roots[i-1], topo.Roots[i]
			if a.Service.Name > b.Service.Name ||
				(a.Service.Name == b.Service.Name && a.Name > b.Name) {
				t.Fatalf("roots not sorted: %s before %s", a.Ref, b.Ref)
			}
		}
	})
}

func TestProperty_BuildTopology_CycleDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Build a config with a deliberate cycle: svc0.op0 -> svc1.op0 -> svc0.op0
		dur0 := rapid.IntRange(1, 100).Draw(t, "dur0")
		dur1 := rapid.IntRange(1, 100).Draw(t, "dur1")

		cfg := &Config{
			Services: []ServiceConfig{
				{
					Name: "svc0",
					Operations: []OperationConfig{{
						Name:     "op0",
						Duration: fmt.Sprintf("%dms", dur0),
						Calls:    []CallConfig{{Target: "svc1.op0"}},
					}},
				},
				{
					Name: "svc1",
					Operations: []OperationConfig{{
						Name:     "op0",
						Duration: fmt.Sprintf("%dms", dur1),
						Calls:    []CallConfig{{Target: "svc0.op0"}},
					}},
				},
			},
			Traffic: TrafficConfig{Rate: "10/s"},
		}

		_, err := BuildTopology(cfg)
		if err == nil {
			t.Fatal("expected cycle detection error, got nil")
		}
	})
}

func TestProperty_BuildTopology_CallsResolvedCorrectly(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}

		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				for _, call := range op.Calls {
					target := call.Operation
					if target == nil {
						t.Fatalf("call from %s has nil target", op.Ref)
					}
					// Verify the target exists in the topology
					targetSvc, ok := topo.Services[target.Service.Name]
					if !ok {
						t.Fatalf("call target %s: service %q not in topology", target.Ref, target.Service.Name)
					}
					if _, ok := targetSvc.Operations[target.Name]; !ok {
						t.Fatalf("call target %s: operation %q not in service %q", target.Ref, target.Name, target.Service.Name)
					}
				}
			}
		}
	})
}

// --- Rate parsing ---

func TestProperty_ParseRate_ValidRates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		count := rapid.IntRange(1, MaxRateCount).Draw(t, "count")
		unit := rapid.SampledFrom([]string{"s", "m", "h"}).Draw(t, "unit")
		s := fmt.Sprintf("%d/%s", count, unit)

		rate, err := ParseRate(s)
		if err != nil {
			t.Fatalf("ParseRate(%q): %v", s, err)
		}
		if rate.Count() != count {
			t.Fatalf("count: got %d, want %d", rate.Count(), count)
		}
	})
}

func TestProperty_ParseRate_ZeroOrNegativeRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		count := rapid.IntRange(-100, 0).Draw(t, "count")
		s := fmt.Sprintf("%d/s", count)

		_, err := ParseRate(s)
		if err == nil {
			t.Fatalf("ParseRate(%q) should fail for non-positive count", s)
		}
	})
}

func TestProperty_ParseRate_ExceedsMaxRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		count := rapid.IntRange(MaxRateCount+1, 100000).Draw(t, "count")
		s := fmt.Sprintf("%d/s", count)

		_, err := ParseRate(s)
		if err == nil {
			t.Fatalf("ParseRate(%q) should fail for count > %d", s, MaxRateCount)
		}
	})
}

func TestProperty_ParseRate_PerSecondRate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		count := rapid.IntRange(1, MaxRateCount).Draw(t, "count")
		s := fmt.Sprintf("%d/s", count)

		rate, err := ParseRate(s)
		if err != nil {
			t.Fatalf("ParseRate(%q): %v", s, err)
		}
		if rate.Period() != time.Second {
			t.Fatalf("expected period Second, got %v", rate.Period())
		}
	})
}

// --- Config validation ---

func TestProperty_ValidateConfig_AcceptsValidConfigs(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := simpleConfig.Draw(t, "cfg")
		if err := ValidateConfig(cfg); err != nil {
			t.Fatalf("ValidateConfig rejected valid config: %v", err)
		}
	})
}

func TestProperty_ValidateConfig_RejectsNoServices(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rate := rapid.IntRange(1, 100).Draw(t, "rate")
		cfg := &Config{
			Traffic: TrafficConfig{Rate: fmt.Sprintf("%d/s", rate)},
		}
		if err := ValidateConfig(cfg); err == nil {
			t.Fatal("expected error for config with no services")
		}
	})
}

func TestProperty_ValidateConfig_RejectsNoTrafficRate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dur := rapid.IntRange(1, 100).Draw(t, "dur")
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: fmt.Sprintf("%dms", dur),
				}},
			}},
		}
		if err := ValidateConfig(cfg); err == nil {
			t.Fatal("expected error for config with no traffic rate")
		}
	})
}

func TestProperty_ValidateConfig_RejectsBadCallTarget(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dur := rapid.IntRange(1, 100).Draw(t, "dur")
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: fmt.Sprintf("%dms", dur),
					Calls:    []CallConfig{{Target: "nonexistent.op"}},
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}
		if err := ValidateConfig(cfg); err == nil {
			t.Fatal("expected error for call to nonexistent target")
		}
	})
}

func TestProperty_ValidateConfig_RejectsBadDuration(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		badDur := rapid.SampledFrom([]string{"", "abc", "-5ms", "0ms"}).Draw(t, "badDur")
		cfg := &Config{
			Services: []ServiceConfig{{
				Name: "svc",
				Operations: []OperationConfig{{
					Name:     "op",
					Duration: badDur,
				}},
			}},
			Traffic: TrafficConfig{Rate: "10/s"},
		}
		if err := ValidateConfig(cfg); err == nil {
			t.Fatalf("expected error for bad duration %q", badDur)
		}
	})
}

// --- Traffic patterns ---

func TestProperty_UniformPattern_ConstantRate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		baseRate := rapid.Float64Range(0.1, 10000).Draw(t, "baseRate")
		p := &UniformPattern{BaseRate: baseRate}

		for range 20 {
			elapsed := time.Duration(rapid.Int64Range(0, int64(24*time.Hour)).Draw(t, "elapsed"))
			got := p.Rate(elapsed)
			if got != baseRate {
				t.Fatalf("UniformPattern.Rate(%v) = %f, want %f", elapsed, got, baseRate)
			}
		}
	})
}

func TestProperty_DiurnalPattern_BoundedRate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		baseRate := rapid.Float64Range(1, 1000).Draw(t, "baseRate")
		trough := rapid.Float64Range(0.1, 0.9).Draw(t, "trough")
		peak := rapid.Float64Range(trough+0.1, 3.0).Draw(t, "peak")
		periodH := rapid.IntRange(1, 48).Draw(t, "periodH")

		p := &DiurnalPattern{
			BaseRate:         baseRate,
			PeakMultiplier:   peak,
			TroughMultiplier: trough,
			Period:           time.Duration(periodH) * time.Hour,
		}

		minRate := baseRate * trough
		maxRate := baseRate * peak

		for range 50 {
			elapsed := time.Duration(rapid.Int64Range(0, int64(72*time.Hour)).Draw(t, "elapsed"))
			got := p.Rate(elapsed)
			if got < minRate-0.001 || got > maxRate+0.001 {
				t.Fatalf("DiurnalPattern.Rate(%v) = %f, outside [%f, %f]", elapsed, got, minRate, maxRate)
			}
		}
	})
}

func TestProperty_BurstyPattern_RateAlternates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		baseRate := rapid.Float64Range(1, 1000).Draw(t, "baseRate")
		multiplier := rapid.Float64Range(2, 10).Draw(t, "multiplier")
		intervalMin := rapid.IntRange(2, 10).Draw(t, "intervalMin")
		durationSec := rapid.IntRange(1, (intervalMin*60)-1).Draw(t, "durationSec")

		p := &BurstyPattern{
			BaseRate:        baseRate,
			BurstMultiplier: multiplier,
			BurstInterval:   time.Duration(intervalMin) * time.Minute,
			BurstDuration:   time.Duration(durationSec) * time.Second,
		}

		// During burst window
		burstElapsed := time.Duration(rapid.IntRange(0, durationSec-1).Draw(t, "burstT")) * time.Second
		burstRate := p.Rate(burstElapsed)
		expected := baseRate * multiplier
		if burstRate != expected {
			t.Fatalf("during burst: got %f, want %f", burstRate, expected)
		}

		// Outside burst window: pick a time halfway between burst end and interval end
		midNormal := time.Duration(durationSec)*time.Second + (time.Duration(intervalMin)*time.Minute-time.Duration(durationSec)*time.Second)/2
		normalRate := p.Rate(midNormal)
		if normalRate != baseRate {
			t.Fatalf("outside burst at %v: got %f, want %f", midNormal, normalRate, baseRate)
		}
	})
}

func TestProperty_TrafficPattern_NonNegativeRate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		patternName := rapid.SampledFrom([]string{"uniform", "diurnal"}).Draw(t, "pattern")
		rate := rapid.IntRange(1, 1000).Draw(t, "rate")

		tcfg := TrafficConfig{
			Rate:    fmt.Sprintf("%d/s", rate),
			Pattern: patternName,
		}
		if patternName == "diurnal" {
			tcfg.PeakMultiplier = 2.0
			tcfg.TroughMultiplier = 0.5
			tcfg.Period = "1h"
		}

		p, err := NewTrafficPattern(tcfg)
		if err != nil {
			t.Fatalf("NewTrafficPattern: %v", err)
		}

		for range 20 {
			elapsed := time.Duration(rapid.Int64Range(0, int64(24*time.Hour)).Draw(t, "elapsed"))
			r := p.Rate(elapsed)
			if r < 0 {
				t.Fatalf("%s pattern returned negative rate %f at %v", patternName, r, elapsed)
			}
		}
	})
}

func TestProperty_CustomPattern_SegmentBoundaries(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		baseRate := rapid.IntRange(1, 100).Draw(t, "baseRate")
		seg1Rate := rapid.IntRange(1, 100).Draw(t, "seg1Rate")
		seg2Rate := rapid.IntRange(1, 100).Draw(t, "seg2Rate")
		seg1UntilMin := rapid.IntRange(1, 10).Draw(t, "seg1Until")
		seg2UntilMin := rapid.IntRange(seg1UntilMin+1, 20).Draw(t, "seg2Until")

		tcfg := TrafficConfig{
			Rate:    fmt.Sprintf("%d/s", baseRate),
			Pattern: "custom",
			Segments: []SegmentConfig{
				{Until: fmt.Sprintf("%dm", seg1UntilMin), Rate: fmt.Sprintf("%d/s", seg1Rate)},
				{Until: fmt.Sprintf("%dm", seg2UntilMin), Rate: fmt.Sprintf("%d/s", seg2Rate)},
			},
		}

		p, err := NewTrafficPattern(tcfg)
		if err != nil {
			t.Fatalf("NewTrafficPattern: %v", err)
		}

		// Before seg1: should return seg1 rate
		r1 := p.Rate(time.Duration(seg1UntilMin-1) * time.Minute)
		if r1 != float64(seg1Rate) {
			t.Fatalf("before seg1 boundary: got %f, want %f", r1, float64(seg1Rate))
		}

		// Between seg1 and seg2: should return seg2 rate
		midpoint := time.Duration((seg1UntilMin+seg2UntilMin)/2) * time.Minute
		if midpoint >= time.Duration(seg2UntilMin)*time.Minute {
			midpoint = time.Duration(seg1UntilMin)*time.Minute + time.Second
		}
		r2 := p.Rate(midpoint)
		if r2 != float64(seg2Rate) {
			t.Fatalf("between segments: got %f at %v, want %f", r2, midpoint, float64(seg2Rate))
		}

		// After all segments: should return base rate
		r3 := p.Rate(time.Duration(seg2UntilMin+1) * time.Minute)
		if r3 != float64(baseRate) {
			t.Fatalf("after all segments: got %f, want %f", r3, float64(baseRate))
		}
	})
}

// --- Circuit breaker state machine ---

// circuitBreakerModel is a simplified model of the expected circuit breaker behaviour.
// rapid drives random sequences of actions (request success, request failure, time advance)
// and we check the real OperationState against this model after every action.
type circuitBreakerModel struct {
	state     *OperationState
	rng       *rand.Rand
	elapsed   time.Duration
	model     CircuitState
	openedAt  time.Duration
	failures  []time.Duration // failure timestamps within window
	threshold int
	window    time.Duration
	cooldown  time.Duration
}

func (m *circuitBreakerModel) pruneFailures() {
	cutoff := m.elapsed - m.window
	pruned := m.failures[:0]
	for _, at := range m.failures {
		if at >= cutoff {
			pruned = append(pruned, at)
		}
	}
	m.failures = pruned
}

func TestProperty_CircuitBreaker_StateMachine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		threshold := rapid.IntRange(1, 5).Draw(t, "threshold")
		windowMs := rapid.IntRange(100, 5000).Draw(t, "windowMs")
		cooldownMs := rapid.IntRange(50, 2000).Draw(t, "cooldownMs")

		os := &OperationState{
			FailureThreshold: threshold,
			WindowDuration:   time.Duration(windowMs) * time.Millisecond,
			Cooldown:         time.Duration(cooldownMs) * time.Millisecond,
		}

		seed := rapid.Uint64().Draw(t, "seed")
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // not used for security

		m := &circuitBreakerModel{
			state:     os,
			rng:       rng,
			threshold: threshold,
			window:    time.Duration(windowMs) * time.Millisecond,
			cooldown:  time.Duration(cooldownMs) * time.Millisecond,
			model:     CircuitClosed,
		}

		t.Repeat(map[string]func(*rapid.T){
			"advanceTime": func(t *rapid.T) {
				advance := time.Duration(rapid.IntRange(1, 1000).Draw(t, "advMs")) * time.Millisecond
				m.elapsed += advance
			},
			"successRequest": func(t *rapid.T) {
				_, _, rejected, reason := m.state.Admit(m.elapsed, m.rng)

				if m.model == CircuitOpen {
					if m.elapsed-m.openedAt >= m.cooldown {
						// Should transition to HalfOpen and admit
						m.model = CircuitHalfOpen
					} else {
						// Should reject
						if !rejected {
							t.Fatal("model says Open, should reject")
						}
						if reason != ReasonCircuitOpen {
							t.Fatalf("expected circuit_open reason, got %q", reason)
						}
						return
					}
				}

				if rejected {
					t.Fatalf("model says %v, should not reject (reason=%q)", m.model, reason)
				}

				m.state.Enter()
				m.state.Exit(m.elapsed, time.Millisecond, false)

				// Model: success in HalfOpen closes the circuit
				if m.model == CircuitHalfOpen {
					m.model = CircuitClosed
					m.failures = m.failures[:0]
				}

				// Prune model's failure window
				m.pruneFailures()
			},
			"failRequest": func(t *rapid.T) {
				_, _, rejected, reason := m.state.Admit(m.elapsed, m.rng)

				if m.model == CircuitOpen {
					if m.elapsed-m.openedAt >= m.cooldown {
						m.model = CircuitHalfOpen
					} else {
						if !rejected {
							t.Fatal("model says Open, should reject")
						}
						if reason != ReasonCircuitOpen {
							t.Fatalf("expected circuit_open reason, got %q", reason)
						}
						return
					}
				}

				if rejected {
					t.Fatalf("model says %v, should not reject (reason=%q)", m.model, reason)
				}

				m.state.Enter()
				m.state.Exit(m.elapsed, time.Millisecond, true)

				// Model: failure in HalfOpen reopens the circuit
				if m.model == CircuitHalfOpen {
					m.model = CircuitOpen
					m.openedAt = m.elapsed
					m.pruneFailures()
					return
				}

				// Model: accumulate failure, prune window, check threshold
				m.pruneFailures()
				if len(m.failures) < m.threshold {
					m.failures = append(m.failures, m.elapsed)
				}

				if len(m.failures) >= m.threshold && m.model == CircuitClosed {
					m.model = CircuitOpen
					m.openedAt = m.elapsed
				}
			},
			"": func(t *rapid.T) {
				// Invariant checks after every action
				if m.state.Circuit != m.model {
					t.Fatalf("state mismatch: real=%v, model=%v (elapsed=%v)",
						m.state.Circuit, m.model, m.elapsed)
				}

				// Open circuit must reject
				if m.model == CircuitOpen && m.elapsed-m.openedAt < m.cooldown {
					_, _, rejected, _ := m.state.Admit(m.elapsed, m.rng)
					if !rejected {
						t.Fatal("Open circuit within cooldown should reject")
					}
				}

				// Active requests should never be negative
				if m.state.ActiveRequests < 0 {
					t.Fatalf("negative active requests: %d", m.state.ActiveRequests)
				}
			},
		})
	})
}
