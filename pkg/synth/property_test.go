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
			dur := rapid.IntRange(1, 100).Draw(t, fmt.Sprintf("dur%d_%d", i, j))
			errPct := rapid.IntRange(0, 100).Draw(t, fmt.Sprintf("errPct%d_%d", i, j))
			ops[j] = OperationConfig{
				Name:     opName,
				Duration: fmt.Sprintf("%dms", dur),
			}
			if errPct > 0 {
				ops[j].ErrorRate = fmt.Sprintf("%d%%", errPct)
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
		Traffic:  TrafficConfig{Rate: "100/s"},
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
	rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // property test

	engine := &Engine{
		Topology: topo,
		Provider: tp,
		Rng:      rng,
	}

	rootOp := topo.Roots[rng.IntN(len(topo.Roots))]
	now := time.Now()
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, &stats, new(int), DefaultMaxSpansPerTrace)

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
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // property test

		overrides := map[string]Override{
			"svc.op": {Duration: Distribution{Mean: time.Duration(overrideDurMs) * time.Millisecond}},
		}

		engine := &Engine{
			Topology: topo,
			Provider: tp,
			Rng:      rng,
		}

		var stats Stats
		engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, overrides, &stats, new(int), DefaultMaxSpansPerTrace)

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
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // property test

		overrides := map[string]Override{
			"svc.op": {ErrorRate: 1.0, HasErrorRate: true},
		}

		engine := &Engine{
			Topology: topo,
			Provider: tp,
			Rng:      rng,
		}

		var stats Stats
		engine.walkTrace(context.Background(), topo.Roots[0], time.Now(), 0, overrides, &stats, new(int), DefaultMaxSpansPerTrace)

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
