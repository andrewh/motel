// Per-operation statistical accumulators for duration, error rate, and call patterns
// Analyses trace trees to collect distributions needed for config generation
package traceimport

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// OpStats accumulates statistics for a single (service, operation) pair.
type OpStats struct {
	DurationCount int
	DurationMean  float64
	DurationM2    float64
	ErrorCount    int
	TotalCount    int
	Calls         map[string]*CallStats // key: "targetService.targetOp"
}

// CallStats tracks how often a downstream call appears.
type CallStats struct {
	Count int // total times this call was observed
}

// CallStyleVote tracks parallel vs sequential votes across traces.
type CallStyleVote struct {
	Parallel   int
	Sequential int
}

// ServiceStats holds per-operation stats and call style votes for one service.
type ServiceStats struct {
	Ops        map[string]*OpStats       // operation name -> stats
	CallStyles map[string]*CallStyleVote // operation name -> voting
}

// StatsCollector accumulates statistics across all traces.
type StatsCollector struct {
	Services map[string]*ServiceStats // service name -> stats
}

// NewStatsCollector creates an empty collector.
func NewStatsCollector() *StatsCollector {
	return &StatsCollector{
		Services: make(map[string]*ServiceStats),
	}
}

// CollectFromTrees walks all trace trees, accumulating per-operation statistics.
func (c *StatsCollector) CollectFromTrees(trees []*TraceTree) {
	for _, tree := range trees {
		for _, root := range tree.Roots {
			c.walkNode(root, nil)
		}
	}
}

// walkNode records statistics for a single operation invocation.
//
// A span nested inside an ancestor of the same (service, operation) — e.g. a DB
// savepoint inside a transaction, or an HTTP client that wraps its request in an
// outer same-named span — is a continuation of that ancestor, not a fresh
// invocation and not a cyclic dependency. Such spans are folded into the
// enclosing operation: walkNode is never called on them, so their duration and
// count are subsumed by the ancestor, and their real downstream calls are
// attributed directly to the operation via effectiveCalls. This keeps the
// inferred topology acyclic (the model forbids cycles) without inflating call
// counts or blending durations across nesting levels.
//
// ancestors holds the "service.operation" refs already on the path to node,
// excluding node itself.
func (c *StatsCollector) walkNode(node *SpanNode, ancestors []string) {
	svc := c.getService(node.Span.Service)
	op := c.getOp(svc, node.Span.Operation)

	duration := node.Span.EndTime.Sub(node.Span.StartTime)
	op.RecordDuration(duration, 1)
	op.TotalCount++
	if node.Span.IsError {
		op.ErrorCount++
	}

	// Path including this node; children matching any ref on it are continuations.
	selfRef := node.Span.Service + "." + node.Span.Operation
	path := make([]string, len(ancestors)+1)
	copy(path, ancestors)
	path[len(ancestors)] = selfRef

	// Effective calls: direct children that are genuine downstream operations,
	// flattening through any continuation spans so their calls attach here.
	calls := effectiveCalls(node, path)

	for _, child := range calls {
		childRef := child.Span.Service + "." + child.Span.Operation
		if op.Calls == nil {
			op.Calls = make(map[string]*CallStats)
		}
		cs := op.Calls[childRef]
		if cs == nil {
			cs = &CallStats{}
			op.Calls[childRef] = cs
		}
		cs.Count++
	}

	// Vote on call style if this operation makes 2+ effective calls.
	if len(calls) >= 2 {
		vote := c.getCallStyle(svc, node.Span.Operation)
		if isParallel(calls) {
			vote.Parallel++
		} else if isSequential(calls) {
			vote.Sequential++
		} else {
			// Ambiguous — count as parallel (engine default)
			vote.Parallel++
		}
	}

	// Recurse into the effective calls; each is itself a genuine invocation.
	for _, child := range calls {
		c.walkNode(child, path)
	}
}

// effectiveCalls returns the spans representing genuine downstream calls from the
// operation rooted at node. A direct child whose (service, operation) is not on
// the path is a real call. A child that is on the path is a continuation of an
// enclosing operation (depth-bounded recursion such as a nested transaction);
// its own real calls are flattened in so they attach to the enclosing operation
// rather than producing a self- or back-edge. path lists the "service.operation"
// refs on the route to node, including node itself.
func effectiveCalls(node *SpanNode, path []string) []*SpanNode {
	var calls []*SpanNode
	for _, child := range node.Children {
		childRef := child.Span.Service + "." + child.Span.Operation
		if containsString(path, childRef) {
			calls = append(calls, effectiveCalls(child, path)...)
		} else {
			calls = append(calls, child)
		}
	}
	return calls
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func (o *OpStats) RecordDuration(d time.Duration, weight int) {
	if weight <= 0 {
		return
	}
	value := float64(d)
	nextCount := o.DurationCount + weight
	delta := value - o.DurationMean
	o.DurationMean += delta * float64(weight) / float64(nextCount)
	o.DurationM2 += delta * (value - o.DurationMean) * float64(weight)
	o.DurationCount = nextCount
}

func (o *OpStats) meanDuration() time.Duration {
	if o.DurationCount > 0 {
		mean := time.Duration(o.DurationMean)
		if mean <= 0 {
			return time.Microsecond
		}
		return mean
	}
	return 0
}

func (o *OpStats) stdDevDuration() time.Duration {
	if o.DurationCount > 1 {
		return time.Duration(math.Sqrt(o.DurationM2 / float64(o.DurationCount-1)))
	}
	if o.DurationCount == 1 {
		return 0
	}
	return 0
}

func (o *OpStats) formatDuration() string {
	return formatDurationStats(o.meanDuration(), o.stdDevDuration())
}

func (c *StatsCollector) getService(name string) *ServiceStats {
	svc, ok := c.Services[name]
	if !ok {
		svc = &ServiceStats{
			Ops:        make(map[string]*OpStats),
			CallStyles: make(map[string]*CallStyleVote),
		}
		c.Services[name] = svc
	}
	return svc
}

func (c *StatsCollector) getOp(svc *ServiceStats, name string) *OpStats {
	op, ok := svc.Ops[name]
	if !ok {
		op = &OpStats{}
		svc.Ops[name] = op
	}
	return op
}

func (c *StatsCollector) getCallStyle(svc *ServiceStats, name string) *CallStyleVote {
	vote, ok := svc.CallStyles[name]
	if !ok {
		vote = &CallStyleVote{}
		svc.CallStyles[name] = vote
	}
	return vote
}

// isParallel checks if children share approximately the same start time (within 1ms).
func isParallel(children []*SpanNode) bool {
	if len(children) < 2 {
		return false
	}
	first := children[0].Span.StartTime
	for _, child := range children[1:] {
		diff := child.Span.StartTime.Sub(first)
		if diff < 0 {
			diff = -diff
		}
		if diff > time.Millisecond {
			return false
		}
	}
	return true
}

// isSequential checks if each child starts after the previous one ends (within 1ms tolerance).
func isSequential(children []*SpanNode) bool {
	if len(children) < 2 {
		return false
	}
	// Sort by start time
	sorted := make([]*SpanNode, len(children))
	copy(sorted, children)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Span.StartTime.Before(sorted[j].Span.StartTime)
	})
	for i := 1; i < len(sorted); i++ {
		gap := sorted[i].Span.StartTime.Sub(sorted[i-1].Span.EndTime)
		if gap < -time.Millisecond {
			return false
		}
	}
	return true
}

func formatDurationStats(mean time.Duration, stddev time.Duration) string {
	meanStr := roundDuration(mean).String()
	if stddev == 0 || float64(stddev) < float64(mean)*0.01 {
		return meanStr
	}
	return meanStr + " +/- " + roundDuration(stddev).String()
}

// roundDuration rounds a duration to a human-friendly precision.
func roundDuration(d time.Duration) time.Duration {
	switch {
	case d >= time.Second:
		return d.Round(100 * time.Millisecond)
	case d >= 100*time.Millisecond:
		return d.Round(10 * time.Millisecond)
	case d >= 10*time.Millisecond:
		return d.Round(time.Millisecond)
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond)
	case d >= 100*time.Microsecond:
		return d.Round(10 * time.Microsecond)
	default:
		return d.Round(time.Microsecond)
	}
}

// FormatErrorRate returns a percentage string like "0.10%" or empty if zero.
func FormatErrorRate(errors, total int) string {
	if errors == 0 || total == 0 {
		return ""
	}
	rate := float64(errors) / float64(total) * 100
	if rate >= 1.0 {
		return fmt.Sprintf("%.0f%%", rate)
	}
	return fmt.Sprintf("%.2f%%", rate)
}
