// Trace tree reconstruction from flat span lists
// Groups spans by trace ID and links children to parents via span IDs
package traceimport

import (
	"fmt"
	"io"
)

// TraceTree holds spans grouped by trace with parent-child links.
type TraceTree struct {
	TraceID  string
	Roots    []*SpanNode
	AllNodes []*SpanNode
}

// SpanNode wraps a Span with its children in the trace tree.
type SpanNode struct {
	Span     Span
	Children []*SpanNode
}

// BuildTrees reconstructs trace trees from a flat list of spans.
// Spans with broken parent references (parent not in dataset) become additional roots.
// Warnings about orphans are written to w (may be nil).
func BuildTrees(spans []Span, w io.Writer) []*TraceTree {
	// Group spans by TraceID
	byTrace := make(map[string][]Span)
	for _, s := range spans {
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}

	trees := make([]*TraceTree, 0, len(byTrace))
	for traceID, traceSpans := range byTrace {
		tree := buildTree(traceID, traceSpans, w)
		trees = append(trees, tree)
	}
	return trees
}

func buildTree(traceID string, spans []Span, w io.Writer) *TraceTree {
	// Index nodes by SpanID
	nodes := make(map[string]*SpanNode, len(spans))
	allNodes := make([]*SpanNode, 0, len(spans))
	for _, s := range spans {
		node := &SpanNode{Span: s}
		nodes[s.SpanID] = node
		allNodes = append(allNodes, node)
	}

	// Link children to parents
	var roots []*SpanNode
	for _, node := range allNodes {
		if node.Span.ParentID == "" {
			roots = append(roots, node)
			continue
		}
		parent, ok := nodes[node.Span.ParentID]
		if !ok {
			// Broken parent reference — treat as root
			if w != nil {
				_, _ = fmt.Fprintf(w, "warning: span %s in trace %s has parent %s not found in dataset, treating as root\n",
					node.Span.SpanID, traceID, node.Span.ParentID)
			}
			roots = append(roots, node)
			continue
		}
		parent.Children = append(parent.Children, node)
	}

	return &TraceTree{
		TraceID:  traceID,
		Roots:    roots,
		AllNodes: allNodes,
	}
}
