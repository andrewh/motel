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
	Durations  []time.Duration
	ErrorCount int
	TotalCount int
	Calls      map[string]*CallStats // key: "targetService.targetOp"
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
			c.walkNode(root)
		}
	}
}

func (c *StatsCollector) walkNode(node *SpanNode) {
	svc := c.getService(node.Span.Service)
	op := c.getOp(svc, node.Span.Operation)

	duration := node.Span.EndTime.Sub(node.Span.StartTime)
	op.Durations = append(op.Durations, duration)
	op.TotalCount++
	if node.Span.IsError {
		op.ErrorCount++
	}

	// Record calls to children
	for _, child := range node.Children {
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

	// Vote on call style if this node has 2+ children
	if len(node.Children) >= 2 {
		vote := c.getCallStyle(svc, node.Span.Operation)
		if isParallel(node.Children) {
			vote.Parallel++
		} else if isSequential(node.Children) {
			vote.Sequential++
		} else {
			// Ambiguous — count as parallel (engine default)
			vote.Parallel++
		}
	}

	// Recurse into children
	for _, child := range node.Children {
		c.walkNode(child)
	}
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

// MeanDuration computes the mean of a duration slice.
// Uses float64 accumulator to avoid int64 overflow on large inputs.
func MeanDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var sum float64
	for _, d := range durations {
		sum += float64(d)
	}
	mean := time.Duration(sum / float64(len(durations)))
	if mean <= 0 {
		return time.Microsecond
	}
	return mean
}

// StdDevDuration computes the sample standard deviation of a duration slice.
func StdDevDuration(durations []time.Duration) time.Duration {
	if len(durations) < 2 {
		return 0
	}
	mean := float64(MeanDuration(durations))
	var sumSq float64
	for _, d := range durations {
		diff := float64(d) - mean
		sumSq += diff * diff
	}
	return time.Duration(math.Sqrt(sumSq / float64(len(durations)-1)))
}

// FormatDuration produces a human-friendly distribution string.
// Returns "Xms +/- Yms" when stddev is significant, or "Xms" when negligible.
func FormatDuration(durations []time.Duration) string {
	mean := MeanDuration(durations)
	stddev := StdDevDuration(durations)

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
