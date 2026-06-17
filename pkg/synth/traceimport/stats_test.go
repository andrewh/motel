// Unit tests for per-operation statistical accumulators
// Covers duration stats, error rates, call style voting, and formatting
package traceimport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOpStatsDurationStats(t *testing.T) {
	var op OpStats
	op.RecordDuration(10*time.Millisecond, 1)
	op.RecordDuration(20*time.Millisecond, 1)
	op.RecordDuration(30*time.Millisecond, 1)

	assert.Equal(t, 3, op.DurationCount)
	assert.Equal(t, 20*time.Millisecond, op.meanDuration())
	stddev := op.stdDevDuration()
	assert.InDelta(t, 10*time.Millisecond, stddev, float64(time.Millisecond))
}

func TestOpStatsDurationStats_Weighted(t *testing.T) {
	var op OpStats
	op.RecordDuration(10*time.Millisecond, 2)
	op.RecordDuration(40*time.Millisecond, 1)

	assert.Equal(t, 3, op.DurationCount)
	assert.Equal(t, 20*time.Millisecond, op.meanDuration())
	assert.Contains(t, op.formatDuration(), "+/-")
}

func TestOpStatsFormatDuration_WithVariance(t *testing.T) {
	var op OpStats
	op.RecordDuration(20*time.Millisecond, 1)
	op.RecordDuration(30*time.Millisecond, 1)
	op.RecordDuration(40*time.Millisecond, 1)

	result := op.formatDuration()
	assert.Contains(t, result, "+/-")
	assert.Contains(t, result, "ms")
}

func TestOpStatsFormatDuration_Fixed(t *testing.T) {
	var op OpStats
	op.RecordDuration(10*time.Millisecond, 1)

	result := op.formatDuration()
	assert.NotContains(t, result, "+/-")
}

func TestFormatErrorRate(t *testing.T) {
	assert.Equal(t, "", FormatErrorRate(0, 100))
	assert.Equal(t, "5%", FormatErrorRate(5, 100))
	assert.Equal(t, "0.50%", FormatErrorRate(1, 200))
}

func TestIsParallel(t *testing.T) {
	now := time.Now()
	children := []*SpanNode{
		{Span: Span{StartTime: now}},
		{Span: Span{StartTime: now.Add(100 * time.Microsecond)}},
	}
	assert.True(t, isParallel(children))
}

func TestIsParallel_Not(t *testing.T) {
	now := time.Now()
	children := []*SpanNode{
		{Span: Span{StartTime: now}},
		{Span: Span{StartTime: now.Add(10 * time.Millisecond)}},
	}
	assert.False(t, isParallel(children))
}

func TestIsSequential(t *testing.T) {
	now := time.Now()
	children := []*SpanNode{
		{Span: Span{StartTime: now, EndTime: now.Add(5 * time.Millisecond)}},
		{Span: Span{StartTime: now.Add(5 * time.Millisecond), EndTime: now.Add(10 * time.Millisecond)}},
	}
	assert.True(t, isSequential(children))
}

func TestIsSequential_Overlapping(t *testing.T) {
	now := time.Now()
	children := []*SpanNode{
		{Span: Span{StartTime: now, EndTime: now.Add(10 * time.Millisecond)}},
		{Span: Span{StartTime: now.Add(5 * time.Millisecond), EndTime: now.Add(15 * time.Millisecond)}},
	}
	assert.False(t, isSequential(children))
}

func TestCollector_Basic(t *testing.T) {
	now := time.Now()
	spans := []Span{
		{TraceID: "t1", SpanID: "root", Service: "gw", Operation: "GET", StartTime: now, EndTime: now.Add(30 * time.Millisecond)},
		{TraceID: "t1", SpanID: "child", ParentID: "root", Service: "api", Operation: "list", StartTime: now.Add(5 * time.Millisecond), EndTime: now.Add(20 * time.Millisecond)},
	}

	trees := BuildTrees(spans, nil)
	collector := NewStatsCollector()
	collector.CollectFromTrees(trees)

	assert.Contains(t, collector.Services, "gw")
	assert.Contains(t, collector.Services, "api")
	assert.Contains(t, collector.Services["gw"].Ops, "GET")
	assert.Equal(t, 1, collector.Services["gw"].Ops["GET"].TotalCount)
	assert.Equal(t, 1, collector.Services["gw"].Ops["GET"].Calls["api.list"].Count)
}

// TestCollector_SkipsSelfNestedEdge ensures that a span nested inside another
// span of the same (service, operation) — e.g. a DB savepoint inside a
// transaction — does not produce a self-referential call edge, which the
// topology model forbids and cycle detection would reject.
func TestCollector_SkipsSelfNestedEdge(t *testing.T) {
	now := time.Now()
	spans := []Span{
		{TraceID: "t1", SpanID: "root", Service: "svc", Operation: "work", StartTime: now, EndTime: now.Add(30 * time.Millisecond)},
		{TraceID: "t1", SpanID: "tx1", ParentID: "root", Service: "svc", Operation: "transaction", StartTime: now.Add(1 * time.Millisecond), EndTime: now.Add(25 * time.Millisecond)},
		{TraceID: "t1", SpanID: "tx2", ParentID: "tx1", Service: "svc", Operation: "transaction", StartTime: now.Add(2 * time.Millisecond), EndTime: now.Add(20 * time.Millisecond)},
		{TraceID: "t1", SpanID: "wr", ParentID: "tx2", Service: "svc", Operation: "db-write", StartTime: now.Add(3 * time.Millisecond), EndTime: now.Add(10 * time.Millisecond)},
	}

	trees := BuildTrees(spans, nil)
	collector := NewStatsCollector()
	collector.CollectFromTrees(trees)

	tx := collector.Services["svc"].Ops["transaction"]
	assert.NotContains(t, tx.Calls, "svc.transaction", "self-referential edge must be skipped")
	assert.Contains(t, tx.Calls, "svc.db-write")
	// Both transaction spans still fold into the same operation's stats.
	assert.Equal(t, 2, tx.TotalCount)
}
