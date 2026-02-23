// Structural analysis of topology graphs
// Computes worst-case depth, fan-out, and span count to catch surprising explosions
package synth

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// CheckResult holds the outcome of a single structural check.
type CheckResult struct {
	Name       string
	Pass       bool
	Limit      int
	Actual     int
	Sampled    *int
	SamplesRun int
	Path       []string
	Ref        string
}

// CheckOptions configures the thresholds and sampling for Check.
type CheckOptions struct {
	MaxDepth         int
	MaxFanOut        int
	MaxSpans         int
	MaxSpansPerTrace int
	Samples          int
	Seed             uint64
}

// SampleResults holds empirical measurements from sampled trace generation.
type SampleResults struct {
	MaxDepth  int
	MaxSpans  int
	MaxFanOut int
	TracesRun int
}

// maxSpansCap prevents overflow in worst-case span multiplication.
const maxSpansCap = math.MaxInt32

// MaxDepth returns the longest path (edge count) from any root to any leaf
// and the operation refs along that path.
//
// Memoisation is safe because BuildTopology guarantees the topology is acyclic.
// In a DAG, a node's subtree depth is the same regardless of which path reaches it,
// so the memo produces correct results under any visited set.
func MaxDepth(topo *Topology) (int, []string) {
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

		for _, call := range op.Calls {
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
	maxFan := 0
	worstRef := ""

	for _, svc := range topo.Services {
		for _, op := range svc.Operations {
			fan := 0
			for _, call := range op.Calls {
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
//
// Memoisation is safe because BuildTopology guarantees the topology is acyclic.
// In a DAG, a node's subtree span count is the same regardless of which path
// reaches it, so the memo produces correct results under any visited set.
func MaxSpans(topo *Topology) (int, string) {
	memo := make(map[*Operation]int)

	var dfs func(op *Operation, visited map[*Operation]bool) int
	dfs = func(op *Operation, visited map[*Operation]bool) int {
		if v, ok := memo[op]; ok {
			return v
		}

		total := 1 // the operation's own span

		for _, call := range op.Calls {
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

	var results SampleResults
	for i := range n {
		exporter.Reset()

		rng := rand.New(rand.NewPCG(seed+uint64(i), 0)) //nolint:gosec // not security-sensitive
		engine := &Engine{
			Topology: topo,
			Provider: tp,
			Rng:      rng,
		}

		root := topo.Roots[rng.IntN(len(topo.Roots))]
		var stats Stats
		spanCount := 0
		engine.walkTrace(context.Background(), root, time.Now(), 0, nil, nil, &stats, &spanCount, maxSpansPerTrace)
		_ = tp.ForceFlush(context.Background())

		spans := exporter.GetSpans()
		results.TracesRun++

		if len(spans) > results.MaxSpans {
			results.MaxSpans = len(spans)
		}

		depth, fanOut := measureTrace(spans)
		if depth > results.MaxDepth {
			results.MaxDepth = depth
		}
		if fanOut > results.MaxFanOut {
			results.MaxFanOut = fanOut
		}
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

// Check runs structural analysis and sampled exploration, returning one result
// per check.
func Check(topo *Topology, opts CheckOptions) []CheckResult {
	staticDepth, depthPath := MaxDepth(topo)
	staticFanOut, fanOutRef := MaxFanOut(topo)
	staticSpans, _ := MaxSpans(topo)

	var sampled SampleResults
	if opts.Samples > 0 {
		sampled = SampleTraces(topo, opts.Samples, opts.Seed, opts.MaxSpansPerTrace)
	}

	results := make([]CheckResult, 0, 3)

	depthResult := CheckResult{
		Name:       "max-depth",
		Pass:       staticDepth <= opts.MaxDepth,
		Limit:      opts.MaxDepth,
		Actual:     staticDepth,
		SamplesRun: sampled.TracesRun,
		Path:       depthPath,
	}
	if opts.Samples > 0 {
		depthResult.Sampled = intPtr(sampled.MaxDepth)
	}
	results = append(results, depthResult)

	fanOutResult := CheckResult{
		Name:       "max-fan-out",
		Pass:       staticFanOut <= opts.MaxFanOut,
		Limit:      opts.MaxFanOut,
		Actual:     staticFanOut,
		SamplesRun: sampled.TracesRun,
		Ref:        fanOutRef,
	}
	if opts.Samples > 0 {
		fanOutResult.Sampled = intPtr(sampled.MaxFanOut)
	}
	results = append(results, fanOutResult)

	spansResult := CheckResult{
		Name:       "max-spans",
		Pass:       staticSpans <= opts.MaxSpans,
		Limit:      opts.MaxSpans,
		Actual:     staticSpans,
		SamplesRun: sampled.TracesRun,
	}
	if opts.Samples > 0 {
		spansResult.Sampled = intPtr(sampled.MaxSpans)
	}
	results = append(results, spansResult)

	return results
}
