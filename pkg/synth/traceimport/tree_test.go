// Unit tests for trace tree reconstruction from flat span lists
// Covers normal trees, multi-root traces, and orphan spans
package traceimport

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTrees_SingleTrace(t *testing.T) {
	spans := []Span{
		{TraceID: "t1", SpanID: "root", ParentID: "", Service: "svc", Operation: "op1", StartTime: time.Now(), EndTime: time.Now()},
		{TraceID: "t1", SpanID: "child1", ParentID: "root", Service: "svc", Operation: "op2", StartTime: time.Now(), EndTime: time.Now()},
		{TraceID: "t1", SpanID: "child2", ParentID: "root", Service: "svc", Operation: "op3", StartTime: time.Now(), EndTime: time.Now()},
	}

	trees := BuildTrees(spans, nil)
	require.Len(t, trees, 1)
	assert.Equal(t, "t1", trees[0].TraceID)
	require.Len(t, trees[0].Roots, 1)
	assert.Equal(t, "root", trees[0].Roots[0].Span.SpanID)
	assert.Len(t, trees[0].Roots[0].Children, 2)
}

func TestBuildTrees_MultipleTraces(t *testing.T) {
	spans := []Span{
		{TraceID: "t1", SpanID: "a", ParentID: "", Service: "svc", Operation: "op1"},
		{TraceID: "t2", SpanID: "b", ParentID: "", Service: "svc", Operation: "op2"},
	}

	trees := BuildTrees(spans, nil)
	assert.Len(t, trees, 2)
}

func TestBuildTrees_OrphanSpan(t *testing.T) {
	var warnings bytes.Buffer
	spans := []Span{
		{TraceID: "t1", SpanID: "root", ParentID: "", Service: "svc", Operation: "op1"},
		{TraceID: "t1", SpanID: "orphan", ParentID: "missing", Service: "svc", Operation: "op2"},
	}

	trees := BuildTrees(spans, &warnings)
	require.Len(t, trees, 1)
	assert.Len(t, trees[0].Roots, 2, "orphan should become an additional root")
	assert.Contains(t, warnings.String(), "not found in dataset")
}

func TestBuildTrees_MultiRoot(t *testing.T) {
	spans := []Span{
		{TraceID: "t1", SpanID: "root1", ParentID: "", Service: "svc1", Operation: "entry1"},
		{TraceID: "t1", SpanID: "root2", ParentID: "", Service: "svc2", Operation: "entry2"},
	}

	trees := BuildTrees(spans, nil)
	require.Len(t, trees, 1)
	assert.Len(t, trees[0].Roots, 2)
}
