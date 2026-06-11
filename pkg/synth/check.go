// Structural analysis of topology graphs
// Computes worst-case depth, fan-out, and span count to catch surprising explosions
package synth

import (
	"context"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// CheckResult holds the outcome of a single structural check.
// Scenarios names the scenario combination that produced the worst case;
// empty means the baseline topology (no scenarios active).
type CheckResult struct {
	Name         string
	Pass         bool
	Limit        int
	Actual       int
	Sampled      *int
	SamplesRun   int
	Path         []string
	Ref          string
	Scenarios    []string
	Distribution *DistributionSummary
}

// DistributionSummary holds percentile statistics for a metric.
type DistributionSummary struct {
	P50 int
	P95 int
	P99 int
	Max int
}

// SampleDistribution collects per-trace metric values across sampled runs.
type SampleDistribution struct {
	Depths  []int
	Spans   []int
	FanOuts []int
}

// Summary computes percentile summaries for each metric.
func (d *SampleDistribution) Summary() (depth, spans, fanOut DistributionSummary) {
	depth = summarise(d.Depths)
	spans = summarise(d.Spans)
	fanOut = summarise(d.FanOuts)
	return depth, spans, fanOut
}

// summarise sorts a copy of data once and extracts p50/p95/p99/max.
func summarise(data []int) DistributionSummary {
	if len(data) == 0 {
		return DistributionSummary{}
	}
	sorted := slices.Clone(data)
	slices.Sort(sorted)
	return DistributionSummary{
		P50: percentileFromSorted(sorted, 50),
		P95: percentileFromSorted(sorted, 95),
		P99: percentileFromSorted(sorted, 99),
		Max: sorted[len(sorted)-1],
	}
}

// percentileFromSorted returns the value at the given percentile (0–100)
// using the nearest-rank method. The input must be non-empty and sorted in
// ascending order.
func percentileFromSorted(sorted []int, p float64) int {
	idx := max(int(math.Ceil(p/100*float64(len(sorted))))-1, 0)
	return sorted[idx]
}

// CheckOptions configures the thresholds and sampling for Check.
// When Scenarios is non-empty, every distinct combination of co-active
// scenarios is checked and the worst case reported per check.
type CheckOptions struct {
	MaxDepth         int
	MaxFanOut        int
	MaxSpans         int
	MaxSpansPerTrace int
	Samples          int
	Seed             uint64
	Scenarios        []Scenario
}

// SampleResults holds empirical measurements from sampled trace generation.
type SampleResults struct {
	MaxDepth     int
	MaxSpans     int
	MaxFanOut    int
	TracesRun    int
	Distribution SampleDistribution
}

// maxSpansCap prevents overflow in worst-case span multiplication.
const maxSpansCap = math.MaxInt32

// MaxDepth returns the longest path (edge count) from any root to any leaf
// and the operation refs along that path.
func MaxDepth(topo *Topology) (int, []string) {
	return maxDepthWith(topo, nil)
}

// maxDepthWith computes MaxDepth with scenario call overrides applied.
//
// Memoisation is safe because BuildTopology guarantees the topology is acyclic
// (and BuildScenarios rejects scenario call changes that create cycles).
// In a DAG, a node's subtree depth is the same regardless of which path reaches it,
// so the memo produces correct results under any visited set.
func maxDepthWith(topo *Topology, overrides map[string]Override) (int, []string) {
	type result struct {
		depth int
		path  []string
	}

	memo := make(map[*Operation]result)

	var dfs func(op *Operation, visited map[*Operation]bool) result
	dfs = func(op *Operation, visited map[*Operation]bool) result {
		if r, ok := memo[op]; ok {
			return r
		}

		best := result{depth: 0, path: []string{op.Ref}}

		for _, call := range effectiveCalls(op, overrides) {
			if visited[call.Operation] {
				continue
			}
			visited[call.Operation] = true
			child := dfs(call.Operation, visited)
			candidate := child.depth + 1
			if candidate > best.depth {
				best.depth = candidate
				best.path = append([]string{op.Ref}, child.path...)
			}
			delete(visited, call.Operation)
		}

		memo[op] = best
		return best
	}

	var maxResult result
	for _, root := range topo.Roots {
		visited := map[*Operation]bool{root: true}
		r := dfs(root, visited)
		if r.depth > maxResult.depth || maxResult.path == nil {
			maxResult = r
		}
	}

	return maxResult.depth, maxResult.path
}

// MaxFanOut returns the worst-case direct children per span and which
// operation produces it. For each operation, it sums
// max(call.Count, 1) * (1 + call.Retries) across all calls.
func MaxFanOut(topo *Topology) (int, string) {
	return maxFanOutWith(topo, nil)
}

// maxFanOutWith computes MaxFanOut with scenario call overrides applied.
func maxFanOutWith(topo *Topology, overrides map[string]Override) (int, string) {
	maxFan := 0
	worstRef := ""

	for _, svc := range topo.Services {
		for _, op := range svc.Operations {
			fan := 0
			for _, call := range effectiveCalls(op, overrides) {
				count := max(call.Count, 1)
				attempts := 1 + call.Retries
				fan += count * attempts
			}
			if fan > maxFan {
				maxFan = fan
				worstRef = op.Ref
			}
		}
	}

	return maxFan, worstRef
}

// MaxSpans returns the worst-case total spans per trace via DFS from each root.
// It multiplies fan-out at each level. Both on-error and on-success paths are
// included (conservative — real traces cannot take both).
// Returns the max and which root produces it.
func MaxSpans(topo *Topology) (int, string) {
	return maxSpansWith(topo, nil)
}

// maxSpansWith computes MaxSpans with scenario call overrides applied.
//
// Memoisation is safe because BuildTopology guarantees the topology is acyclic
// (and BuildScenarios rejects scenario call changes that create cycles).
// In a DAG, a node's subtree span count is the same regardless of which path
// reaches it, so the memo produces correct results under any visited set.
func maxSpansWith(topo *Topology, overrides map[string]Override) (int, string) {
	memo := make(map[*Operation]int)

	var dfs func(op *Operation, visited map[*Operation]bool) int
	dfs = func(op *Operation, visited map[*Operation]bool) int {
		if v, ok := memo[op]; ok {
			return v
		}

		total := 1 // the operation's own span

		for _, call := range effectiveCalls(op, overrides) {
			if visited[call.Operation] {
				continue
			}
			visited[call.Operation] = true
			childSpans := dfs(call.Operation, visited)
			delete(visited, call.Operation)

			count := max(call.Count, 1)
			attempts := 1 + call.Retries
			fanForCall := count * attempts

			// Guard the multiplication against overflow before comparing.
			if childSpans > 0 && fanForCall > (maxSpansCap-total)/childSpans {
				total = maxSpansCap
				break
			}
			total += fanForCall * childSpans
		}

		if total > maxSpansCap {
			total = maxSpansCap
		}
		memo[op] = total
		return total
	}

	maxTotal := 0
	worstRoot := ""
	for _, root := range topo.Roots {
		visited := map[*Operation]bool{root: true}
		n := dfs(root, visited)
		if n > maxTotal {
			maxTotal = n
			worstRoot = root.Ref
		}
	}

	return maxTotal, worstRoot
}

// SampleTraces runs the engine n times with an in-memory exporter and measures
// empirical depth, fan-out, and span count. Observed span counts are bounded by
// maxSpansPerTrace (or DefaultMaxSpansPerTrace when 0), so for topologies whose
// static worst-case exceeds that limit, the observed value will plateau.
func SampleTraces(topo *Topology, n int, seed uint64, maxSpansPerTrace int) SampleResults {
	return sampleTracesWith(topo, n, seed, maxSpansPerTrace, nil)
}

// sampleTracesWith runs SampleTraces with scenario overrides applied to every trace.
func sampleTracesWith(topo *Topology, n int, seed uint64, maxSpansPerTrace int, overrides map[string]Override) SampleResults {
	if maxSpansPerTrace <= 0 {
		maxSpansPerTrace = DefaultMaxSpansPerTrace
	}

	if seed == 0 {
		seed = rand.Uint64() //nolint:gosec // not security-sensitive
	}

	if len(topo.Roots) == 0 || n == 0 {
		return SampleResults{}
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	results := SampleResults{
		Distribution: SampleDistribution{
			Depths:  make([]int, 0, n),
			Spans:   make([]int, 0, n),
			FanOuts: make([]int, 0, n),
		},
	}
	for i := range n {
		exporter.Reset()

		rng := rand.New(rand.NewPCG(seed+uint64(i), 0)) //nolint:gosec // not security-sensitive
		engine := &Engine{
			Topology: topo,
			Tracers:  func(name string) trace.Tracer { return tp.Tracer("github.com/andrewh/motel") },
			Rng:      rng,
		}

		root := topo.Roots[rng.IntN(len(topo.Roots))]
		var stats Stats
		spanCount := 0
		engine.walkTrace(context.Background(), root, time.Now(), 0, overrides, nil, &stats, &spanCount, maxSpansPerTrace, false)
		_ = tp.ForceFlush(context.Background())

		spans := exporter.GetSpans()
		results.TracesRun++

		nSpans := len(spans)
		if nSpans > results.MaxSpans {
			results.MaxSpans = nSpans
		}

		depth, fanOut := measureTrace(spans)
		if depth > results.MaxDepth {
			results.MaxDepth = depth
		}
		if fanOut > results.MaxFanOut {
			results.MaxFanOut = fanOut
		}

		results.Distribution.Depths = append(results.Distribution.Depths, depth)
		results.Distribution.Spans = append(results.Distribution.Spans, nSpans)
		results.Distribution.FanOuts = append(results.Distribution.FanOuts, fanOut)
	}

	return results
}

// measureTrace computes the depth and max fan-out from exported spans.
func measureTrace(spans []tracetest.SpanStub) (depth, fanOut int) {
	if len(spans) == 0 {
		return 0, 0
	}

	children := make(map[trace.SpanID][]trace.SpanID)
	var roots []trace.SpanID

	for _, s := range spans {
		sid := s.SpanContext.SpanID()
		pid := s.Parent.SpanID()
		if !pid.IsValid() {
			roots = append(roots, sid)
		} else {
			children[pid] = append(children[pid], sid)
		}
	}

	// Measure max children per span
	for _, kids := range children {
		if len(kids) > fanOut {
			fanOut = len(kids)
		}
	}

	// Measure depth via BFS from roots
	type entry struct {
		id    trace.SpanID
		depth int
	}
	queue := make([]entry, 0, len(spans))
	for _, r := range roots {
		queue = append(queue, entry{r, 0})
	}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		if e.depth > depth {
			depth = e.depth
		}
		for _, kid := range children[e.id] {
			queue = append(queue, entry{kid, e.depth + 1})
		}
	}

	return depth, fanOut
}

// intPtr returns a pointer to v.
func intPtr(v int) *int { return &v }

// ScenarioSet is a distinct combination of scenarios that are active together
// at some point in the simulation timeline. The zero value is the baseline
// (no scenarios active).
type ScenarioSet struct {
	Names     []string
	Overrides map[string]Override
}

// ScenarioSets enumerates the distinct combinations of co-active scenarios by
// evaluating the active set at every window boundary — active sets only change
// when a scenario starts or ends, so the boundaries cover every combination
// that occurs in time. The baseline (no scenarios) is always first.
func ScenarioSets(scenarios []Scenario) []ScenarioSet {
	sets := []ScenarioSet{{}}
	if len(scenarios) == 0 {
		return sets
	}

	boundaries := make([]time.Duration, 0, len(scenarios)*2)
	for _, sc := range scenarios {
		boundaries = append(boundaries, sc.Start, sc.End)
	}
	slices.Sort(boundaries)
	boundaries = slices.Compact(boundaries)

	seen := map[string]bool{"": true}
	for _, t := range boundaries {
		active := ActiveScenarios(scenarios, t)
		if len(active) == 0 {
			continue
		}
		names := make([]string, len(active))
		for i, sc := range active {
			names[i] = sc.Name
		}
		key := strings.Join(names, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		sets = append(sets, ScenarioSet{Names: names, Overrides: ResolveOverrides(active)})
	}
	return sets
}

// setEvaluation holds the static and sampled measurements for one scenario set.
type setEvaluation struct {
	names     []string
	depth     int
	depthPath []string
	fanOut    int
	fanOutRef string
	spans     int
	sampled   SampleResults
}

// Check runs structural analysis and sampled exploration, returning one result
// per check. When opts.Scenarios is non-empty, the baseline topology and every
// distinct combination of co-active scenarios are evaluated; each result
// reports the worst case and the scenario combination that produced it
// (selected by static value, with the sampled maximum as tie-breaker).
func Check(topo *Topology, opts CheckOptions) []CheckResult {
	sets := ScenarioSets(opts.Scenarios)

	// Fix the seed once so every scenario set samples the same trace sequence.
	seed := opts.Seed
	if opts.Samples > 0 && seed == 0 {
		seed = rand.Uint64() //nolint:gosec // not security-sensitive
	}

	evals := make([]setEvaluation, 0, len(sets))
	for _, set := range sets {
		ev := setEvaluation{names: set.Names}
		ev.depth, ev.depthPath = maxDepthWith(topo, set.Overrides)
		ev.fanOut, ev.fanOutRef = maxFanOutWith(topo, set.Overrides)
		ev.spans, _ = maxSpansWith(topo, set.Overrides)
		if opts.Samples > 0 {
			ev.sampled = sampleTracesWith(topo, opts.Samples, seed, opts.MaxSpansPerTrace, set.Overrides)
		}
		evals = append(evals, ev)
	}

	// worst returns the evaluation with the highest static value for a check.
	// Ties go to the sampled maximum, then to the earlier set (baseline first).
	worst := func(static, sampledMax func(setEvaluation) int) setEvaluation {
		best := evals[0]
		for _, ev := range evals[1:] {
			if static(ev) > static(best) ||
				(static(ev) == static(best) && opts.Samples > 0 && sampledMax(ev) > sampledMax(best)) {
				best = ev
			}
		}
		return best
	}

	depthEval := worst(
		func(e setEvaluation) int { return e.depth },
		func(e setEvaluation) int { return e.sampled.MaxDepth })
	fanOutEval := worst(
		func(e setEvaluation) int { return e.fanOut },
		func(e setEvaluation) int { return e.sampled.MaxFanOut })
	spansEval := worst(
		func(e setEvaluation) int { return e.spans },
		func(e setEvaluation) int { return e.sampled.MaxSpans })

	results := make([]CheckResult, 0, 3)

	depthResult := CheckResult{
		Name:       "max-depth",
		Pass:       depthEval.depth <= opts.MaxDepth,
		Limit:      opts.MaxDepth,
		Actual:     depthEval.depth,
		SamplesRun: depthEval.sampled.TracesRun,
		Path:       depthEval.depthPath,
		Scenarios:  depthEval.names,
	}
	if opts.Samples > 0 {
		depthResult.Sampled = intPtr(depthEval.sampled.MaxDepth)
		dd := summarise(depthEval.sampled.Distribution.Depths)
		depthResult.Distribution = &dd
	}
	results = append(results, depthResult)

	fanOutResult := CheckResult{
		Name:       "max-fan-out",
		Pass:       fanOutEval.fanOut <= opts.MaxFanOut,
		Limit:      opts.MaxFanOut,
		Actual:     fanOutEval.fanOut,
		SamplesRun: fanOutEval.sampled.TracesRun,
		Ref:        fanOutEval.fanOutRef,
		Scenarios:  fanOutEval.names,
	}
	if opts.Samples > 0 {
		fanOutResult.Sampled = intPtr(fanOutEval.sampled.MaxFanOut)
		fd := summarise(fanOutEval.sampled.Distribution.FanOuts)
		fanOutResult.Distribution = &fd
	}
	results = append(results, fanOutResult)

	spansResult := CheckResult{
		Name:       "max-spans",
		Pass:       spansEval.spans <= opts.MaxSpans,
		Limit:      opts.MaxSpans,
		Actual:     spansEval.spans,
		SamplesRun: spansEval.sampled.TracesRun,
		Scenarios:  spansEval.names,
	}
	if opts.Samples > 0 {
		spansResult.Sampled = intPtr(spansEval.sampled.MaxSpans)
		sd := summarise(spansEval.sampled.Distribution.Spans)
		spansResult.Distribution = &sd
	}
	results = append(results, spansResult)

	return results
}
