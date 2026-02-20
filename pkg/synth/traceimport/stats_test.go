// Unit tests for per-operation statistical accumulators
// Covers duration stats, error rates, call style voting, and formatting
package traceimport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMeanDuration(t *testing.T) {
	durations := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	assert.Equal(t, 20*time.Millisecond, MeanDuration(durations))
}

func TestMeanDuration_Empty(t *testing.T) {
	assert.Equal(t, time.Duration(0), MeanDuration(nil))
}

func TestStdDevDuration(t *testing.T) {
	durations := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	stddev := StdDevDuration(durations)
	assert.InDelta(t, 10*time.Millisecond, stddev, float64(time.Millisecond))
}

func TestStdDevDuration_Single(t *testing.T) {
	assert.Equal(t, time.Duration(0), StdDevDuration([]time.Duration{5 * time.Millisecond}))
}

func TestFormatDuration_WithVariance(t *testing.T) {
	durations := []time.Duration{20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond}
	result := FormatDuration(durations)
	assert.Contains(t, result, "+/-")
	assert.Contains(t, result, "ms")
}

func TestFormatDuration_Fixed(t *testing.T) {
	durations := []time.Duration{10 * time.Millisecond}
	result := FormatDuration(durations)
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
