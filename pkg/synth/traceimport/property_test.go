// Property-based tests for the import pipeline using pgregory.net/rapid
// Covers tree construction invariants, stats consistency, duration arithmetic,
// and marshal round-trip validation
package traceimport

import (
	"fmt"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"pgregory.net/rapid"
)

// --- Generators ---

// genSpanID produces a hex-like span ID.
func genSpanID(t *rapid.T, label string) string {
	return fmt.Sprintf("%s-%04x", label, rapid.IntRange(0, 0xffff).Draw(t, label))
}

// genSpan produces a single Span with controlled randomness.
func genSpan(t *rapid.T, traceID, spanID, parentID, service, operation string) Span {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	startOffset := rapid.Int64Range(0, int64(10*time.Second)).Draw(t, "startOffset")
	duration := rapid.Int64Range(int64(time.Microsecond), int64(time.Second)).Draw(t, "duration")
	isError := rapid.Bool().Draw(t, "isError")
	start := base.Add(time.Duration(startOffset))
	end := start.Add(time.Duration(duration))
	return Span{
		TraceID:   traceID,
		SpanID:    spanID,
		ParentID:  parentID,
		Service:   service,
		Operation: operation,
		StartTime: start,
		EndTime:   end,
		IsError:   isError,
	}
}

// drawServiceName draws a service name from a small pool.
func drawServiceName(t *rapid.T, label string) string {
	return rapid.SampledFrom([]string{"gateway", "api", "db", "cache", "auth"}).Draw(t, label)
}

// drawOperationName draws an operation name from a small pool.
func drawOperationName(t *rapid.T, label string) string {
	return rapid.SampledFrom([]string{"GET", "POST", "query", "lookup", "verify"}).Draw(t, label)
}

// genTree generates a well-formed trace tree as a flat span list.
// Starts with a root and adds children by randomly selecting an existing span as parent.
func genTree(t *rapid.T) []Span {
	traceID := "trace-001"
	n := rapid.IntRange(1, 20).Draw(t, "treeSize")

	rootSvc := drawServiceName(t, "rootSvc")
	rootOp := drawOperationName(t, "rootOp")
	rootID := genSpanID(t, "span0")
	spans := []Span{genSpan(t, traceID, rootID, "", rootSvc, rootOp)}

	for i := 1; i < n; i++ {
		parentIdx := rapid.IntRange(0, len(spans)-1).Draw(t, fmt.Sprintf("parent%d", i))
		spanID := genSpanID(t, fmt.Sprintf("span%d", i))
		svc := drawServiceName(t, fmt.Sprintf("svc%d", i))
		op := drawOperationName(t, fmt.Sprintf("op%d", i))
		spans = append(spans, genSpan(t, traceID, spanID, spans[parentIdx].SpanID, svc, op))
	}
	return spans
}

// genDurations generates a non-empty slice of positive durations.
func genDurations(t *rapid.T) []time.Duration {
	n := rapid.IntRange(1, 1000).Draw(t, "durCount")
	ds := make([]time.Duration, n)
	for i := range ds {
		ds[i] = time.Duration(rapid.Int64Range(int64(time.Microsecond), int64(10*time.Second)).Draw(t, fmt.Sprintf("dur%d", i)))
	}
	return ds
}

func recordDurations(ds []time.Duration) OpStats {
	var op OpStats
	for _, d := range ds {
		op.RecordDuration(d, 1)
	}
	return op
}

// --- Tree invariants ---

func TestProperty_BuildTrees_AllSpansPresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)

		total := 0
		for _, tree := range trees {
			total += len(tree.AllNodes)
		}
		if total != len(spans) {
			t.Fatalf("expected %d nodes, got %d", len(spans), total)
		}
	})
}

func TestProperty_BuildTrees_SingleRoot(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)

		if len(trees) != 1 {
			t.Fatalf("expected 1 tree, got %d", len(trees))
		}
		if len(trees[0].Roots) != 1 {
			t.Fatalf("expected 1 root, got %d", len(trees[0].Roots))
		}
	})
}

func TestProperty_BuildTrees_EveryNodeReachable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)

		for _, tree := range trees {
			reachable := make(map[string]bool)
			var walk func(n *SpanNode)
			walk = func(n *SpanNode) {
				reachable[n.Span.SpanID] = true
				for _, c := range n.Children {
					walk(c)
				}
			}
			for _, root := range tree.Roots {
				walk(root)
			}
			for _, node := range tree.AllNodes {
				if !reachable[node.Span.SpanID] {
					t.Fatalf("span %s not reachable from any root", node.Span.SpanID)
				}
			}
		}
	})
}

func TestProperty_BuildTrees_NoCycles(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)

		for _, tree := range trees {
			for _, root := range tree.Roots {
				visited := make(map[string]bool)
				var walk func(n *SpanNode) bool
				walk = func(n *SpanNode) bool {
					if visited[n.Span.SpanID] {
						return false
					}
					visited[n.Span.SpanID] = true
					for _, c := range n.Children {
						if !walk(c) {
							return false
						}
					}
					return true
				}
				if !walk(root) {
					t.Fatal("cycle detected in tree")
				}
			}
		}
	})
}

// --- Stats invariants ---

func TestProperty_Stats_CountsMatchSpans(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)
		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		// Each span is counted once, except continuations — spans nested inside an
		// ancestor of the same (service, operation), which are folded into the
		// enclosing operation. The total count must equal the number of genuine
		// (non-continuation) spans.
		ref := func(n *SpanNode) string { return n.Span.Service + "." + n.Span.Operation }
		expectedCount := 0
		var walk func(n *SpanNode, path []string)
		walk = func(n *SpanNode, path []string) {
			self := ref(n)
			if !containsString(path, self) {
				expectedCount++
			}
			childPath := append(append([]string{}, path...), self)
			for _, child := range n.Children {
				walk(child, childPath)
			}
		}
		for _, tree := range trees {
			for _, root := range tree.Roots {
				walk(root, nil)
			}
		}

		totalCounted := 0
		for _, svcStats := range collector.Services {
			for _, opStats := range svcStats.Ops {
				totalCounted += opStats.TotalCount
			}
		}
		if totalCounted != expectedCount {
			t.Fatalf("stats counted %d genuine spans, expected %d (of %d input spans)",
				totalCounted, expectedCount, len(spans))
		}
	})
}

func TestProperty_Stats_ErrorCountBounded(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)
		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		for svcName, svcStats := range collector.Services {
			for opName, opStats := range svcStats.Ops {
				if opStats.ErrorCount > opStats.TotalCount {
					t.Fatalf("%s.%s: error count %d > total count %d", svcName, opName, opStats.ErrorCount, opStats.TotalCount)
				}
			}
		}
	})
}

func TestProperty_Stats_DurationCountMatchesCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)
		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		for svcName, svcStats := range collector.Services {
			for opName, opStats := range svcStats.Ops {
				if opStats.DurationCount != opStats.TotalCount {
					t.Fatalf("%s.%s: duration count %d != total count %d", svcName, opName, opStats.DurationCount, opStats.TotalCount)
				}
			}
		}
	})
}

func TestProperty_Stats_DurationMeanPositive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)
		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		for svcName, svcStats := range collector.Services {
			for opName, opStats := range svcStats.Ops {
				if opStats.DurationCount > 0 && opStats.meanDuration() < 0 {
					t.Fatalf("%s.%s: mean duration %v is negative", svcName, opName, opStats.meanDuration())
				}
			}
		}
	})
}

func TestProperty_Stats_CallCountsMatchChildren(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)
		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		// Independent reference, applying the same continuation-folding spec by a
		// different formulation. An edge parent->C is a call only when C's
		// (service, operation) is not already on the path to C (otherwise C is a
		// continuation of an enclosing operation), and it is attributed to the
		// owner of the parent — the nearest ancestor-or-self that is itself a
		// genuine invocation. Edges are tallied per owner *node*, so Count is the
		// number of distinct genuine invocations that made the call and
		// Occurrences is the total number of call spans.
		ref := func(n *SpanNode) string { return n.Span.Service + "." + n.Span.Operation }
		perNode := make(map[*SpanNode]map[string]int) // ownerNode -> target -> occurrences
		var walk func(n *SpanNode, path []string, owner *SpanNode)
		walk = func(n *SpanNode, path []string, owner *SpanNode) {
			self := ref(n)
			if !containsString(path, self) {
				owner = n // n is a genuine invocation; it owns its real calls
			}
			for _, child := range n.Children {
				cref := ref(child)
				if cref == self || containsString(path, cref) {
					continue // continuation of n or an ancestor: not a call edge
				}
				if perNode[owner] == nil {
					perNode[owner] = make(map[string]int)
				}
				perNode[owner][cref]++
			}
			childPath := append(append([]string{}, path...), self)
			for _, child := range n.Children {
				walk(child, childPath, owner)
			}
		}
		for _, tree := range trees {
			for _, root := range tree.Roots {
				walk(root, nil, nil)
			}
		}

		// Aggregate per owner node into per-operation invocation and occurrence
		// counts.
		expInv := make(map[string]map[string]int)
		expOcc := make(map[string]map[string]int)
		for owner, targets := range perNode {
			op := ref(owner)
			if expInv[op] == nil {
				expInv[op] = make(map[string]int)
				expOcc[op] = make(map[string]int)
			}
			for target, occ := range targets {
				expInv[op][target]++
				expOcc[op][target] += occ
			}
		}

		// Verify stats match the reference in both directions.
		seen := make(map[string]map[string]bool)
		for svcName, svcStats := range collector.Services {
			for opName, opStats := range svcStats.Ops {
				self := svcName + "." + opName
				if _, ok := opStats.Calls[self]; ok {
					t.Fatalf("%s has a self-referential call edge", self)
				}
				seen[self] = make(map[string]bool)
				for target, cs := range opStats.Calls {
					seen[self][target] = true
					if cs.Count != expInv[self][target] {
						t.Fatalf("%s -> %s: invocation count %d, expected %d",
							self, target, cs.Count, expInv[self][target])
					}
					if cs.Occurrences != expOcc[self][target] {
						t.Fatalf("%s -> %s: occurrences %d, expected %d",
							self, target, cs.Occurrences, expOcc[self][target])
					}
				}
			}
		}
		for op, targets := range expInv {
			for target := range targets {
				if !seen[op][target] {
					t.Fatalf("%s -> %s: missing from stats", op, target)
				}
			}
		}
	})
}

// --- Duration arithmetic ---

func TestProperty_DurationMean_BetweenMinMax(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ds := genDurations(t)
		op := recordDurations(ds)
		mean := op.meanDuration()

		min, max := ds[0], ds[0]
		for _, d := range ds[1:] {
			if d < min {
				min = d
			}
			if d > max {
				max = d
			}
		}
		if mean < min || mean > max {
			t.Fatalf("mean %v not in [%v, %v]", mean, min, max)
		}
	})
}

func TestProperty_DurationMean_Idempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ds := genDurations(t)
		op := recordDurations(ds)
		m1 := op.meanDuration()
		m2 := op.meanDuration()
		if m1 != m2 {
			t.Fatalf("duration mean not idempotent: %v != %v", m1, m2)
		}
	})
}

func TestProperty_DurationMean_Uniform(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := time.Duration(rapid.Int64Range(int64(time.Microsecond), int64(10*time.Second)).Draw(t, "d"))
		n := rapid.IntRange(1, 100).Draw(t, "n")
		var op OpStats
		op.RecordDuration(d, n)
		mean := op.meanDuration()
		if mean != d {
			t.Fatalf("mean of %d identical %v values = %v", n, d, mean)
		}
	})
}

func TestProperty_DurationStdDev_NonNegative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ds := genDurations(t)
		op := recordDurations(ds)
		sd := op.stdDevDuration()
		if sd < 0 {
			t.Fatalf("stddev = %v is negative", sd)
		}
	})
}

func TestProperty_DurationStdDev_ZeroForUniform(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := time.Duration(rapid.Int64Range(int64(time.Microsecond), int64(10*time.Second)).Draw(t, "d"))
		n := rapid.IntRange(2, 100).Draw(t, "n")
		var op OpStats
		op.RecordDuration(d, n)
		sd := op.stdDevDuration()
		if sd != 0 {
			t.Fatalf("stddev of %d identical %v values = %v, expected 0", n, d, sd)
		}
	})
}

// --- Marshal round-trip ---

// genMultiTraceSpans generates spans across multiple traces with a proper tree
// structure for each trace. This ensures MarshalConfig gets realistic input
// with multiple traces (needed for traffic rate computation).
func genMultiTraceSpans(t *rapid.T) []Span {
	nTraces := rapid.IntRange(2, 10).Draw(t, "nTraces")
	var allSpans []Span

	for i := range nTraces {
		traceID := fmt.Sprintf("trace-%04d", i)
		n := rapid.IntRange(1, 10).Draw(t, fmt.Sprintf("treeSize%d", i))

		rootSvc := drawServiceName(t, fmt.Sprintf("rootSvc%d", i))
		rootOp := drawOperationName(t, fmt.Sprintf("rootOp%d", i))
		rootID := genSpanID(t, fmt.Sprintf("t%d-span0", i))
		spans := []Span{genSpan(t, traceID, rootID, "", rootSvc, rootOp)}

		for j := 1; j < n; j++ {
			parentIdx := rapid.IntRange(0, len(spans)-1).Draw(t, fmt.Sprintf("t%d-parent%d", i, j))
			spanID := genSpanID(t, fmt.Sprintf("t%d-span%d", i, j))
			svc := drawServiceName(t, fmt.Sprintf("t%d-svc%d", i, j))
			op := drawOperationName(t, fmt.Sprintf("t%d-op%d", i, j))
			spans = append(spans, genSpan(t, traceID, spanID, spans[parentIdx].SpanID, svc, op))
		}
		allSpans = append(allSpans, spans...)
	}
	return allSpans
}

func TestProperty_MarshalConfig_ProducesValidTopology(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genMultiTraceSpans(t)
		trees := BuildTrees(spans, nil)

		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		serviceAttrs := inferServiceAttributes(spans)
		windowSecs := computeWindow(trees)

		yamlBytes, err := MarshalConfig(collector, serviceAttrs, len(trees), len(spans), windowSecs)
		if err != nil {
			t.Fatalf("MarshalConfig: %v", err)
		}

		cfg, err := synth.ParseConfig(yamlBytes)
		if err != nil {
			t.Fatalf("ParseConfig failed on generated YAML:\n%s\nerror: %v", yamlBytes, err)
		}
		if err := synth.ValidateConfig(cfg); err != nil {
			t.Fatalf("ValidateConfig failed on generated YAML:\n%s\nerror: %v", yamlBytes, err)
		}
	})
}

func TestProperty_MarshalConfig_ContainsAllServices(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genMultiTraceSpans(t)
		trees := BuildTrees(spans, nil)

		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		serviceAttrs := inferServiceAttributes(spans)
		windowSecs := computeWindow(trees)

		yamlBytes, err := MarshalConfig(collector, serviceAttrs, len(trees), len(spans), windowSecs)
		if err != nil {
			t.Fatalf("MarshalConfig: %v", err)
		}

		cfg, err := synth.ParseConfig(yamlBytes)
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}

		// Every service in stats should appear in the config
		cfgServices := make(map[string]bool)
		for _, svc := range cfg.Services {
			cfgServices[svc.Name] = true
		}
		for svcName := range collector.Services {
			if !cfgServices[svcName] {
				t.Fatalf("service %q in stats but not in generated config", svcName)
			}
		}
	})
}
