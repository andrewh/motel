package hola

import (
	"fmt"
	"math"
	"testing"

	"pgregory.net/rapid"
)

const (
	propMaxNodes = 10
	propMinSize  = 10.0
	propMaxSize  = 120.0
)

// propLayout is the property shared by the rapid test and the fuzz
// target: DoHOLA on any valid graph terminates with finite, overlap-free
// node positions and orthogonal routes for every edge.
func propLayout(t *rapid.T) {
	n := rapid.IntRange(1, propMaxNodes).Draw(t, "n")
	g := NewGraph()
	for i := 0; i < n; i++ {
		w := rapid.Float64Range(propMinSize, propMaxSize).Draw(t, fmt.Sprintf("w%d", i))
		h := rapid.Float64Range(propMinSize, propMaxSize).Draw(t, fmt.Sprintf("h%d", i))
		if _, err := g.AddNode(fmt.Sprintf("n%d", i), w, h); err != nil {
			t.Fatalf("AddNode: %v", err)
		}
	}
	maxEdges := n * (n - 1) / 2
	m := rapid.IntRange(0, maxEdges).Draw(t, "m")
	for k := 0; k < m; k++ {
		i := rapid.IntRange(0, n-1).Draw(t, fmt.Sprintf("ei%d", k))
		j := rapid.IntRange(0, n-1).Draw(t, fmt.Sprintf("ej%d", k))
		if i == j {
			continue
		}
		// Skip duplicates rather than failing the draw.
		if _, err := g.AddEdge(fmt.Sprintf("n%d", i), fmt.Sprintf("n%d", j)); err != nil {
			continue
		}
	}

	if err := DoHOLA(g); err != nil {
		t.Fatalf("DoHOLA: %v", err)
	}
	if g.NumEdges() == 0 {
		return
	}

	const tol = 1e-6
	nodes := g.Nodes()
	for _, nd := range nodes {
		if math.IsNaN(nd.X) || math.IsNaN(nd.Y) || math.IsInf(nd.X, 0) || math.IsInf(nd.Y, 0) {
			t.Fatalf("node %s has non-finite position (%g, %g)", nd.ID, nd.X, nd.Y)
		}
	}
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			a, b := nodes[i], nodes[j]
			ox := (a.W+b.W)/2 - math.Abs(a.X-b.X)
			oy := (a.H+b.H)/2 - math.Abs(a.Y-b.Y)
			if ox > tol && oy > tol {
				t.Errorf("nodes %s and %s overlap by (%g, %g)", a.ID, b.ID, ox, oy)
			}
		}
	}
	for _, e := range g.Edges() {
		if len(e.Route) < 2 {
			t.Errorf("edge %s-%s has no route", e.Src, e.Dst)
			continue
		}
		for k := 1; k < len(e.Route); k++ {
			p, q := e.Route[k-1], e.Route[k]
			if math.Abs(p.X-q.X) > tol && math.Abs(p.Y-q.Y) > tol {
				t.Errorf("edge %s-%s: non-orthogonal segment %v -> %v", e.Src, e.Dst, p, q)
			}
		}
	}
}

func TestPropLayout(t *testing.T) {
	rapid.Check(t, propLayout)
}
