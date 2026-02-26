package synth

import (
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// --- Unit tests for static analysis ---

func TestMaxDepth_LinearChain(t *testing.T) {
	// A → B → C → D: depth 3
	d := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opD := &Operation{Service: d, Name: "D", Ref: "s.D"}
	opC := &Operation{Service: d, Name: "C", Ref: "s.C", Calls: []Call{{Operation: opD}}}
	opB := &Operation{Service: d, Name: "B", Ref: "s.B", Calls: []Call{{Operation: opC}}}
	opA := &Operation{Service: d, Name: "A", Ref: "s.A", Calls: []Call{{Operation: opB}}}
	d.Operations["A"] = opA
	d.Operations["B"] = opB
	d.Operations["C"] = opC
	d.Operations["D"] = opD

	topo := &Topology{
		Services: map[string]*Service{"s": d},
		Roots:    []*Operation{opA},
	}

	depth, path := MaxDepth(topo)
	if depth != 3 {
		t.Fatalf("expected depth 3, got %d", depth)
	}
	if len(path) != 4 {
		t.Fatalf("expected path length 4, got %d: %v", len(path), path)
	}
}

func TestMaxDepth_Diamond(t *testing.T) {
	// A → {B, C} → D: depth 2
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opD := &Operation{Service: s, Name: "D", Ref: "s.D"}
	opB := &Operation{Service: s, Name: "B", Ref: "s.B", Calls: []Call{{Operation: opD}}}
	opC := &Operation{Service: s, Name: "C", Ref: "s.C", Calls: []Call{{Operation: opD}}}
	opA := &Operation{Service: s, Name: "A", Ref: "s.A", Calls: []Call{{Operation: opB}, {Operation: opC}}}
	s.Operations["A"] = opA
	s.Operations["B"] = opB
	s.Operations["C"] = opC
	s.Operations["D"] = opD

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{opA},
	}

	depth, _ := MaxDepth(topo)
	if depth != 2 {
		t.Fatalf("expected depth 2, got %d", depth)
	}
}

func TestMaxFanOut_WithRetries(t *testing.T) {
	// Operation with count:3, retries:2 → fan-out = 3 * (1+2) = 9
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	target := &Operation{Service: s, Name: "target", Ref: "s.target"}
	caller := &Operation{
		Service: s, Name: "caller", Ref: "s.caller",
		Calls: []Call{{Operation: target, Count: 3, Retries: 2}},
	}
	s.Operations["target"] = target
	s.Operations["caller"] = caller

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{caller},
	}

	fan, ref := MaxFanOut(topo)
	if fan != 9 {
		t.Fatalf("expected fan-out 9, got %d", fan)
	}
	if ref != "s.caller" {
		t.Fatalf("expected worst ref s.caller, got %s", ref)
	}
}

func TestMaxSpans_FanOutTree(t *testing.T) {
	// Root calls A with count:2, A calls B with count:2
	// Spans: 1 (root) + 2 (A instances) + 2*2 (B instances) = 7
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opB := &Operation{Service: s, Name: "B", Ref: "s.B"}
	opA := &Operation{Service: s, Name: "A", Ref: "s.A", Calls: []Call{{Operation: opB, Count: 2}}}
	root := &Operation{Service: s, Name: "root", Ref: "s.root", Calls: []Call{{Operation: opA, Count: 2}}}
	s.Operations["root"] = root
	s.Operations["A"] = opA
	s.Operations["B"] = opB

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{root},
	}

	spans, _ := MaxSpans(topo)
	if spans != 7 {
		t.Fatalf("expected 7 spans, got %d", spans)
	}
}

func TestMaxSpans_WithRetries(t *testing.T) {
	// Root calls A (count:1, retries:1): attempts = 2
	// A calls B (count:1, retries:0): attempts = 1
	// Spans: 1 (root) + 2*(1 + 1) = 5
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opB := &Operation{Service: s, Name: "B", Ref: "s.B"}
	opA := &Operation{Service: s, Name: "A", Ref: "s.A", Calls: []Call{{Operation: opB}}}
	root := &Operation{
		Service: s, Name: "root", Ref: "s.root",
		Calls: []Call{{Operation: opA, Retries: 1}},
	}
	s.Operations["root"] = root
	s.Operations["A"] = opA
	s.Operations["B"] = opB

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{root},
	}

	spans, _ := MaxSpans(topo)
	// root: 1 span
	// call to A with retries:1 → 2 attempts, each A has 1+1(B) = 2 spans
	// total: 1 + 2*2 = 5
	if spans != 5 {
		t.Fatalf("expected 5 spans, got %d", spans)
	}
}

func TestMaxDepth_SingleNode(t *testing.T) {
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	op := &Operation{Service: s, Name: "op", Ref: "s.op"}
	s.Operations["op"] = op

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{op},
	}

	depth, path := MaxDepth(topo)
	if depth != 0 {
		t.Fatalf("expected depth 0, got %d", depth)
	}
	if len(path) != 1 || path[0] != "s.op" {
		t.Fatalf("expected path [s.op], got %v", path)
	}
}

func TestCheck_PassesWithGenerousLimits(t *testing.T) {
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opB := &Operation{Service: s, Name: "B", Ref: "s.B",
		Duration: Distribution{Mean: 10 * time.Millisecond}}
	opA := &Operation{Service: s, Name: "A", Ref: "s.A",
		Duration: Distribution{Mean: 10 * time.Millisecond},
		Calls:    []Call{{Operation: opB}}}
	s.Operations["A"] = opA
	s.Operations["B"] = opB

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{opA},
	}

	results := Check(topo, CheckOptions{
		MaxDepth:  10,
		MaxFanOut: 100,
		MaxSpans:  10000,
		Samples:   10,
		Seed:      42,
	})

	for _, r := range results {
		if !r.Pass {
			t.Fatalf("check %s should pass, got FAIL (actual=%d, limit=%d)", r.Name, r.Actual, r.Limit)
		}
	}
}

func TestCheck_FailsOnTightDepthLimit(t *testing.T) {
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opC := &Operation{Service: s, Name: "C", Ref: "s.C",
		Duration: Distribution{Mean: 10 * time.Millisecond}}
	opB := &Operation{Service: s, Name: "B", Ref: "s.B",
		Duration: Distribution{Mean: 10 * time.Millisecond},
		Calls:    []Call{{Operation: opC}}}
	opA := &Operation{Service: s, Name: "A", Ref: "s.A",
		Duration: Distribution{Mean: 10 * time.Millisecond},
		Calls:    []Call{{Operation: opB}}}
	s.Operations["A"] = opA
	s.Operations["B"] = opB
	s.Operations["C"] = opC

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{opA},
	}

	results := Check(topo, CheckOptions{
		MaxDepth:  1,
		MaxFanOut: 100,
		MaxSpans:  10000,
		Samples:   0,
	})

	if results[0].Pass {
		t.Fatal("depth check should fail with limit 1 and actual depth 2")
	}
}

func TestMaxDepth_EmptyTopology(t *testing.T) {
	topo := &Topology{
		Services: map[string]*Service{},
	}

	depth, path := MaxDepth(topo)
	if depth != 0 {
		t.Fatalf("expected depth 0, got %d", depth)
	}
	if path != nil {
		t.Fatalf("expected nil path, got %v", path)
	}
}

func TestMaxFanOut_EmptyTopology(t *testing.T) {
	topo := &Topology{
		Services: map[string]*Service{},
	}

	fan, ref := MaxFanOut(topo)
	if fan != 0 {
		t.Fatalf("expected fan-out 0, got %d", fan)
	}
	if ref != "" {
		t.Fatalf("expected empty ref, got %q", ref)
	}
}

func TestMaxSpans_EmptyTopology(t *testing.T) {
	topo := &Topology{
		Services: map[string]*Service{},
	}

	spans, ref := MaxSpans(topo)
	if spans != 0 {
		t.Fatalf("expected 0 spans, got %d", spans)
	}
	if ref != "" {
		t.Fatalf("expected empty ref, got %q", ref)
	}
}

func TestSampleTraces_ZeroSamples(t *testing.T) {
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	op := &Operation{Service: s, Name: "op", Ref: "s.op",
		Duration: Distribution{Mean: 10 * time.Millisecond}}
	s.Operations["op"] = op

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{op},
	}

	results := SampleTraces(topo, 0, 42, 0)
	if results.TracesRun != 0 {
		t.Fatalf("expected 0 traces run, got %d", results.TracesRun)
	}
	if results.MaxSpans != 0 {
		t.Fatalf("expected 0 max spans, got %d", results.MaxSpans)
	}
}

func TestSampleTraces_CustomSpanCap(t *testing.T) {
	// Build a topology where uncapped traces produce more than 5 spans:
	// root calls A with count:3, so worst case = 1 + 3 = 4, but with
	// A calling B we get 1 + 3*(1+1) = 7. Cap at 5 to verify capping.
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opB := &Operation{Service: s, Name: "B", Ref: "s.B",
		Duration: Distribution{Mean: 10 * time.Millisecond}}
	opA := &Operation{Service: s, Name: "A", Ref: "s.A",
		Duration: Distribution{Mean: 10 * time.Millisecond},
		Calls:    []Call{{Operation: opB}}}
	root := &Operation{Service: s, Name: "root", Ref: "s.root",
		Duration: Distribution{Mean: 10 * time.Millisecond},
		Calls:    []Call{{Operation: opA, Count: 3}}}
	s.Operations["root"] = root
	s.Operations["A"] = opA
	s.Operations["B"] = opB

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{root},
	}

	// Without cap: should observe 7 spans (1 root + 3*A + 3*B)
	uncapped := SampleTraces(topo, 100, 42, 0)
	if uncapped.MaxSpans != 7 {
		t.Fatalf("expected 7 uncapped spans, got %d", uncapped.MaxSpans)
	}

	// With cap of 5: observed should not exceed 5
	capped := SampleTraces(topo, 100, 42, 5)
	if capped.MaxSpans > 5 {
		t.Fatalf("expected at most 5 capped spans, got %d", capped.MaxSpans)
	}
}

func TestCheck_EmptyTopology(t *testing.T) {
	topo := &Topology{
		Services: map[string]*Service{},
	}

	results := Check(topo, CheckOptions{
		MaxDepth:  10,
		MaxFanOut: 100,
		MaxSpans:  10000,
		Samples:   0,
	})

	for _, r := range results {
		if !r.Pass {
			t.Fatalf("check %s should pass on empty topology, got FAIL", r.Name)
		}
	}
}

// --- percentile and distribution tests ---

func TestSummarise_Empty(t *testing.T) {
	s := summarise(nil)
	if s.P50 != 0 || s.P95 != 0 || s.P99 != 0 || s.Max != 0 {
		t.Fatalf("expected all zeros for empty input, got %+v", s)
	}
}

func TestSummarise_SingleElement(t *testing.T) {
	s := summarise([]int{7})
	if s.P50 != 7 || s.P95 != 7 || s.P99 != 7 || s.Max != 7 {
		t.Fatalf("expected all 7 for single element, got %+v", s)
	}
}

func TestPercentileFromSorted_Zero(t *testing.T) {
	sorted := []int{1, 1, 3, 4, 5}
	if got := percentileFromSorted(sorted, 0); got != 1 {
		t.Fatalf("p0: expected minimum (1), got %d", got)
	}
}

func TestSummarise_KnownDistribution(t *testing.T) {
	// 1..100
	data := make([]int, 100)
	for i := range data {
		data[i] = i + 1
	}
	s := summarise(data)
	if s.P50 != 50 {
		t.Fatalf("p50: expected 50, got %d", s.P50)
	}
	if s.P95 != 95 {
		t.Fatalf("p95: expected 95, got %d", s.P95)
	}
	if s.P99 != 99 {
		t.Fatalf("p99: expected 99, got %d", s.P99)
	}
	if s.Max != 100 {
		t.Fatalf("max: expected 100, got %d", s.Max)
	}
}

func TestSummarise_DoesNotMutateInput(t *testing.T) {
	data := []int{5, 3, 1, 4, 2}
	orig := make([]int, len(data))
	copy(orig, data)
	_ = summarise(data)
	for i := range data {
		if data[i] != orig[i] {
			t.Fatalf("input was mutated at index %d: got %d, want %d", i, data[i], orig[i])
		}
	}
}

func TestSampleTraces_PopulatesDistribution(t *testing.T) {
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opB := &Operation{Service: s, Name: "B", Ref: "s.B",
		Duration: Distribution{Mean: 10 * time.Millisecond}}
	opA := &Operation{Service: s, Name: "A", Ref: "s.A",
		Duration: Distribution{Mean: 10 * time.Millisecond},
		Calls:    []Call{{Operation: opB}}}
	s.Operations["A"] = opA
	s.Operations["B"] = opB

	topo := &Topology{
		Services: map[string]*Service{"s": s},
		Roots:    []*Operation{opA},
	}

	results := SampleTraces(topo, 50, 42, 0)
	if len(results.Distribution.Depths) != 50 {
		t.Fatalf("expected 50 depth samples, got %d", len(results.Distribution.Depths))
	}
	if len(results.Distribution.Spans) != 50 {
		t.Fatalf("expected 50 span samples, got %d", len(results.Distribution.Spans))
	}
	if len(results.Distribution.FanOuts) != 50 {
		t.Fatalf("expected 50 fan-out samples, got %d", len(results.Distribution.FanOuts))
	}
}

// --- Property tests: static bounds >= sampled observations ---
//
// Static analysis computes the worst case over all possible executions: it
// includes both on-error and on-success branches (mutually exclusive at
// runtime), assumes every retry fires (only happens on errors), and assumes
// every probabilistic call fires. This produces the supremum of all possible
// trace shapes.
//
// A sampled trace is one realisation drawn from the distribution over trace
// shapes. The maximum observed across N samples is a lower bound on the true
// supremum — it can approach the static bound but can never exceed it, because
// no single execution can take more paths than the static analysis accounts for.
//
// If the invariant breaks, either the static analysis undercounts or the engine
// produces traces the static model doesn't account for — both are real defects.

// genRealisticConfig generates topologies with shape distributions matching
// production microservice call graphs, based on measurements from:
//
//	Du et al., "DGG: A Novel Framework for Microservice Call Graph
//	Generation Based on Realistic Distributions" (ICWS 2024).
//
// Key distributions from the paper used here:
//   - Call graph depth: >99.99% have depth ≤ 6, max observed ~15
//   - Fan-out per service: 1–10 children (Alibaba), up to 50 (Meta)
//   - Repeated calls (count): median 3–5, P99 reaches 374–469
//   - 48.8% of services have >2 interfaces (operations)
//
// The generator builds a DAG by layering services into depth tiers:
// tier 0 is roots, tier 1 is called by tier 0, etc. Each operation in
// tier N calls operations in tier N+1 only, guaranteeing acyclicity.
func genRealisticConfig(t *rapid.T) *Config {
	type svcOp struct{ svc, op string }

	nSvcs := rapid.IntRange(5, 50).Draw(t, "nSvcs")
	depth := rapid.IntRange(1, 8).Draw(t, "depth")

	svcNames := make([]string, nSvcs)
	for i := range nSvcs {
		svcNames[i] = fmt.Sprintf("svc%d", i)
	}

	// Assign each service to a depth tier. Tier 0 always has at least one
	// service. When nSvcs > depth, distribute one service per tier first to
	// guarantee the graph can actually reach the drawn depth; remaining
	// services are assigned randomly.
	tiers := make([]int, nSvcs)
	for i := 0; i <= depth && i < nSvcs; i++ {
		tiers[i] = i
	}
	for i := depth + 1; i < nSvcs; i++ {
		tiers[i] = rapid.IntRange(0, depth).Draw(t, fmt.Sprintf("tier%d", i))
	}

	// Operations per service: 1–4, uniform (P(>2) = 50%, close to the paper's 48.8%).
	opsPerSvcGen := rapid.IntRange(1, 4)

	// Build services and collect ops by tier
	tierOps := make(map[int][]svcOp)
	svcs := make([]ServiceConfig, nSvcs)
	for i, svcName := range svcNames {
		nOps := opsPerSvcGen.Draw(t, fmt.Sprintf("nOps%d", i))
		ops := make([]OperationConfig, nOps)
		for j := range nOps {
			opName := fmt.Sprintf("op%d", j)
			dur := genDurationString.Draw(t, fmt.Sprintf("dur%d_%d", i, j))
			ops[j] = OperationConfig{
				Name:     opName,
				Duration: dur,
			}
			if rapid.Bool().Draw(t, fmt.Sprintf("hasErr%d_%d", i, j)) {
				ops[j].ErrorRate = genErrorRateString.Draw(t, fmt.Sprintf("errRate%d_%d", i, j))
			}
			tierOps[tiers[i]] = append(tierOps[tiers[i]], svcOp{svcName, opName})
		}
		svcs[i] = ServiceConfig{Name: svcName, Operations: ops}
	}

	// Build a map from service name to its index in svcs for call wiring
	svcIdx := make(map[string]int, nSvcs)
	for i, name := range svcNames {
		svcIdx[name] = i
	}

	// Fan-out per operation: 1–6, long-tailed (concentrated at low end).
	// The paper reports 1–10 (Alibaba) up to 50 (Meta); truncated at 6 here
	// to keep generated traces tractable.
	fanOutGen := rapid.SampledFrom([]int{1, 1, 1, 1, 2, 2, 2, 3, 3, 4, 5, 6})

	// Call count (repeats): usually 1, occasionally 2–5.
	countGen := rapid.SampledFrom([]int{1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 3, 5})

	// Wire calls: each operation in tier N calls operations in tier N+1.
	for tier := 0; tier < depth; tier++ {
		targets := tierOps[tier+1]
		if len(targets) == 0 {
			continue
		}
		for _, caller := range tierOps[tier] {
			si := svcIdx[caller.svc]
			var oi int
			for k, op := range svcs[si].Operations {
				if op.Name == caller.op {
					oi = k
					break
				}
			}

			fanOut := fanOutGen.Draw(t, fmt.Sprintf("fan%d_%s_%s", tier, caller.svc, caller.op))
			nCalls := min(fanOut, len(targets))
			if nCalls == 0 {
				continue
			}

			shuffled := rapid.Permutation(targets).Draw(t, fmt.Sprintf("perm%d_%s_%s", tier, caller.svc, caller.op))
			calls := make([]CallConfig, nCalls)
			for c := range nCalls {
				call := CallConfig{Target: shuffled[c].svc + "." + shuffled[c].op}

				call.Count = countGen.Draw(t, fmt.Sprintf("cnt%d_%s_%s_%d", tier, caller.svc, caller.op, c))

				if rapid.Bool().Draw(t, fmt.Sprintf("hasRetries%d_%s_%s_%d", tier, caller.svc, caller.op, c)) {
					call.Retries = rapid.IntRange(1, 2).Draw(t, fmt.Sprintf("retries%d_%s_%s_%d", tier, caller.svc, caller.op, c))
				}
				if rapid.Bool().Draw(t, fmt.Sprintf("hasProb%d_%s_%s_%d", tier, caller.svc, caller.op, c)) {
					call.Probability = rapid.Float64Range(0.1, 1.0).Draw(t, fmt.Sprintf("prob%d_%s_%s_%d", tier, caller.svc, caller.op, c))
				}
				if rapid.Bool().Draw(t, fmt.Sprintf("hasCond%d_%s_%s_%d", tier, caller.svc, caller.op, c)) {
					call.Condition = rapid.SampledFrom([]string{"on-error", "on-success"}).Draw(t, fmt.Sprintf("cond%d_%s_%s_%d", tier, caller.svc, caller.op, c))
				}

				calls[c] = call
			}
			svcs[si].Operations[oi].Calls = calls
		}
	}

	return &Config{
		Services: svcs,
		Traffic:  TrafficConfig{Rate: genRateString.Draw(t, "rate")},
	}
}

// genCheckConfig generates a valid Config with count, retries, probability, and condition.
func genCheckConfig(t *rapid.T) *Config {
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

	// Add calls with count, retries, probability, and condition
	for i := range svcs {
		for j := range svcs[i].Operations {
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
			shuffled := rapid.Permutation(targets).Draw(t, fmt.Sprintf("perm%d_%d", i, j))
			calls := make([]CallConfig, nCalls)
			for c := range nCalls {
				call := CallConfig{Target: shuffled[c].svc + "." + shuffled[c].op}

				if rapid.Bool().Draw(t, fmt.Sprintf("hasCount%d_%d_%d", i, j, c)) {
					call.Count = rapid.IntRange(1, 3).Draw(t, fmt.Sprintf("count%d_%d_%d", i, j, c))
				}
				if rapid.Bool().Draw(t, fmt.Sprintf("hasRetries%d_%d_%d", i, j, c)) {
					call.Retries = rapid.IntRange(1, 2).Draw(t, fmt.Sprintf("retries%d_%d_%d", i, j, c))
				}
				if rapid.Bool().Draw(t, fmt.Sprintf("hasProb%d_%d_%d", i, j, c)) {
					p := rapid.Float64Range(0.1, 1.0).Draw(t, fmt.Sprintf("prob%d_%d_%d", i, j, c))
					call.Probability = p
				}
				if rapid.Bool().Draw(t, fmt.Sprintf("hasCond%d_%d_%d", i, j, c)) {
					call.Condition = rapid.SampledFrom([]string{"on-error", "on-success"}).Draw(t, fmt.Sprintf("cond%d_%d_%d", i, j, c))
				}
				if rapid.Bool().Draw(t, fmt.Sprintf("hasTimeout%d_%d_%d", i, j, c)) {
					call.Timeout = rapid.SampledFrom([]string{"100ms", "1s", "5s"}).Draw(t, fmt.Sprintf("timeout%d_%d_%d", i, j, c))
				}
				if call.Retries > 0 && rapid.Bool().Draw(t, fmt.Sprintf("hasBackoff%d_%d_%d", i, j, c)) {
					call.RetryBackoff = rapid.SampledFrom([]string{"10ms", "50ms", "100ms"}).Draw(t, fmt.Sprintf("backoff%d_%d_%d", i, j, c))
				}

				calls[c] = call
			}
			svcs[i].Operations[j].Calls = calls
		}
	}

	return &Config{
		Services: svcs,
		Traffic:  TrafficConfig{Rate: genRateString.Draw(t, "rate")},
	}
}

func TestProperty_MaxDepth_BoundsObserved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genCheckConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		staticDepth, _ := MaxDepth(topo)
		sampled := SampleTraces(topo, 100, rapid.Uint64().Draw(t, "seed"), 0)

		if sampled.MaxDepth > staticDepth {
			t.Fatalf("sampled depth %d exceeds static bound %d", sampled.MaxDepth, staticDepth)
		}
	})
}

func TestProperty_MaxSpans_BoundsObserved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genCheckConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		staticSpans, _ := MaxSpans(topo)
		sampled := SampleTraces(topo, 100, rapid.Uint64().Draw(t, "seed"), 0)

		if sampled.MaxSpans > staticSpans {
			t.Fatalf("sampled spans %d exceeds static bound %d", sampled.MaxSpans, staticSpans)
		}
	})
}

func TestProperty_MaxFanOut_BoundsObserved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genCheckConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		staticFanOut, _ := MaxFanOut(topo)
		sampled := SampleTraces(topo, 100, rapid.Uint64().Draw(t, "seed"), 0)

		if sampled.MaxFanOut > staticFanOut {
			t.Fatalf("sampled fan-out %d exceeds static bound %d", sampled.MaxFanOut, staticFanOut)
		}
	})
}

func TestProperty_DistributionOrdering(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genCheckConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		sampled := SampleTraces(topo, 100, rapid.Uint64().Draw(t, "seed"), 0)
		depthDist, spansDist, fanOutDist := sampled.Distribution.Summary()

		for _, tc := range []struct {
			name string
			dist DistributionSummary
			max  int
		}{
			{"depth", depthDist, sampled.MaxDepth},
			{"spans", spansDist, sampled.MaxSpans},
			{"fan-out", fanOutDist, sampled.MaxFanOut},
		} {
			if tc.dist.P50 > tc.dist.P95 {
				t.Fatalf("%s: p50 (%d) > p95 (%d)", tc.name, tc.dist.P50, tc.dist.P95)
			}
			if tc.dist.P95 > tc.dist.P99 {
				t.Fatalf("%s: p95 (%d) > p99 (%d)", tc.name, tc.dist.P95, tc.dist.P99)
			}
			if tc.dist.P99 > tc.dist.Max {
				t.Fatalf("%s: p99 (%d) > max (%d)", tc.name, tc.dist.P99, tc.dist.Max)
			}
			if tc.dist.Max != tc.max {
				t.Fatalf("%s: distribution max (%d) != MaxX (%d)", tc.name, tc.dist.Max, tc.max)
			}
		}
	})
}

// TestRealisticStaticBoundsHold verifies that for any generated topology using
// production-scale distributions, Check() succeeds when given its own static
// bounds as limits. This confirms MaxDepth, MaxFanOut, and MaxSpans are
// self-consistent — the static analysis never underestimates.
func TestRealisticStaticBoundsHold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genRealisticConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		staticDepth, _ := MaxDepth(topo)
		staticFanOut, _ := MaxFanOut(topo)
		staticSpans, _ := MaxSpans(topo)

		results := Check(topo, CheckOptions{
			MaxDepth:  staticDepth,
			MaxFanOut: staticFanOut,
			MaxSpans:  staticSpans,
			Samples:   0,
		})

		for _, r := range results {
			if !r.Pass {
				t.Fatalf("check %s should pass with own static bounds (actual=%d, limit=%d)",
					r.Name, r.Actual, r.Limit)
			}
		}
	})
}

// TestRealisticSampledWithinStatic verifies that for any generated topology
// using production-scale distributions, sampled observed values never exceed
// the static worst-case bounds. This is the key invariant from the existing
// per-metric property tests but exercised on much larger, more realistic graphs.
func TestRealisticSampledWithinStatic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genRealisticConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		staticDepth, _ := MaxDepth(topo)
		staticFanOut, _ := MaxFanOut(topo)
		staticSpans, _ := MaxSpans(topo)

		sampled := SampleTraces(topo, 50, rapid.Uint64().Draw(t, "seed"), 0)

		if sampled.MaxDepth > staticDepth {
			t.Fatalf("sampled depth %d exceeds static bound %d", sampled.MaxDepth, staticDepth)
		}
		if sampled.MaxFanOut > staticFanOut {
			t.Fatalf("sampled fan-out %d exceeds static bound %d", sampled.MaxFanOut, staticFanOut)
		}
		if sampled.MaxSpans > staticSpans {
			t.Fatalf("sampled spans %d exceeds static bound %d", sampled.MaxSpans, staticSpans)
		}
	})
}
