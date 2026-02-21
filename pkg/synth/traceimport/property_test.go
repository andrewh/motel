// Property-based tests for the import pipeline using pgregory.net/rapid
// Covers tree construction invariants, stats consistency, duration arithmetic,
// and marshal round-trip validation
package traceimport

import (
	"fmt"
	"os"
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

		// Total span count across all stats must equal input span count
		totalCounted := 0
		for _, svcStats := range collector.Services {
			for _, opStats := range svcStats.Ops {
				totalCounted += opStats.TotalCount
			}
		}
		if totalCounted != len(spans) {
			t.Fatalf("stats counted %d spans, input had %d", totalCounted, len(spans))
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

func TestProperty_Stats_DurationsLengthMatchesCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)
		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		for svcName, svcStats := range collector.Services {
			for opName, opStats := range svcStats.Ops {
				if len(opStats.Durations) != opStats.TotalCount {
					t.Fatalf("%s.%s: durations length %d != total count %d", svcName, opName, len(opStats.Durations), opStats.TotalCount)
				}
			}
		}
	})
}

func TestProperty_Stats_DurationsPositive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spans := genTree(t)
		trees := BuildTrees(spans, nil)
		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		for svcName, svcStats := range collector.Services {
			for opName, opStats := range svcStats.Ops {
				for i, d := range opStats.Durations {
					if d < 0 {
						t.Fatalf("%s.%s: duration[%d] = %v is negative", svcName, opName, i, d)
					}
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

		// Compute expected call counts by walking trees directly
		type parentKey struct{ svc, op string }
		expected := make(map[parentKey]map[string]int) // parent -> target -> count
		var walk func(n *SpanNode)
		walk = func(n *SpanNode) {
			pk := parentKey{n.Span.Service, n.Span.Operation}
			if expected[pk] == nil {
				expected[pk] = make(map[string]int)
			}
			for _, child := range n.Children {
				ref := child.Span.Service + "." + child.Span.Operation
				expected[pk][ref]++
			}
			for _, child := range n.Children {
				walk(child)
			}
		}
		for _, tree := range trees {
			for _, root := range tree.Roots {
				walk(root)
			}
		}

		// Verify stats match
		for svcName, svcStats := range collector.Services {
			for opName, opStats := range svcStats.Ops {
				pk := parentKey{svcName, opName}
				for target, cs := range opStats.Calls {
					exp := expected[pk][target]
					if cs.Count != exp {
						t.Fatalf("%s.%s -> %s: got count %d, expected %d",
							svcName, opName, target, cs.Count, exp)
					}
				}
			}
		}
	})
}

// --- Duration arithmetic ---

func TestProperty_MeanDuration_BetweenMinMax(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ds := genDurations(t)
		mean := MeanDuration(ds)

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

func TestProperty_MeanDuration_Idempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ds := genDurations(t)
		m1 := MeanDuration(ds)
		m2 := MeanDuration(ds)
		if m1 != m2 {
			t.Fatalf("MeanDuration not idempotent: %v != %v", m1, m2)
		}
	})
}

func TestProperty_MeanDuration_Uniform(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := time.Duration(rapid.Int64Range(int64(time.Microsecond), int64(10*time.Second)).Draw(t, "d"))
		n := rapid.IntRange(1, 100).Draw(t, "n")
		ds := make([]time.Duration, n)
		for i := range ds {
			ds[i] = d
		}
		mean := MeanDuration(ds)
		if mean != d {
			t.Fatalf("mean of %d identical %v values = %v", n, d, mean)
		}
	})
}

func TestProperty_StdDevDuration_NonNegative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ds := genDurations(t)
		sd := StdDevDuration(ds)
		if sd < 0 {
			t.Fatalf("stddev = %v is negative", sd)
		}
	})
}

func TestProperty_StdDevDuration_ZeroForUniform(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := time.Duration(rapid.Int64Range(int64(time.Microsecond), int64(10*time.Second)).Draw(t, "d"))
		n := rapid.IntRange(2, 100).Draw(t, "n")
		ds := make([]time.Duration, n)
		for i := range ds {
			ds[i] = d
		}
		sd := StdDevDuration(ds)
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

		// Write to temp file for LoadConfig (it reads from disk)
		f, err := os.CreateTemp("", "property-test-*.yaml")
		if err != nil {
			t.Fatalf("creating temp file: %v", err)
		}
		defer os.Remove(f.Name())

		if _, err := f.Write(yamlBytes); err != nil {
			f.Close()
			t.Fatalf("writing temp file: %v", err)
		}
		f.Close()

		cfg, err := synth.LoadConfig(f.Name())
		if err != nil {
			t.Fatalf("LoadConfig failed on generated YAML:\n%s\nerror: %v", yamlBytes, err)
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

		f, err := os.CreateTemp("", "property-test-*.yaml")
		if err != nil {
			t.Fatalf("creating temp file: %v", err)
		}
		defer os.Remove(f.Name())

		if _, err := f.Write(yamlBytes); err != nil {
			f.Close()
			t.Fatalf("writing temp file: %v", err)
		}
		f.Close()

		cfg, err := synth.LoadConfig(f.Name())
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
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
