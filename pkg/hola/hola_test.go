package hola

import (
	"math"
	"testing"
)

const testNodeW, testNodeH = 60.0, 30.0

func buildGraph(t *testing.T, nodes []string, edges [][2]string) *Graph {
	t.Helper()
	g := NewGraph()
	for _, id := range nodes {
		if _, err := g.AddNode(id, testNodeW, testNodeH); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	for _, e := range edges {
		if _, err := g.AddEdge(e[0], e[1]); err != nil {
			t.Fatalf("AddEdge(%v): %v", e, err)
		}
	}
	return g
}

func assertLayoutInvariants(t *testing.T, g *Graph) {
	t.Helper()
	nodes := g.Nodes()
	for _, n := range nodes {
		if math.IsNaN(n.X) || math.IsNaN(n.Y) || math.IsInf(n.X, 0) || math.IsInf(n.Y, 0) {
			t.Fatalf("node %s has non-finite position (%g, %g)", n.ID, n.X, n.Y)
		}
	}
	const tol = 1e-6
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
				t.Errorf("edge %s-%s has non-orthogonal segment %v -> %v", e.Src, e.Dst, p, q)
			}
		}
		assertOnBoundary(t, g.Node(e.Src), e.Route[0])
		assertOnBoundary(t, g.Node(e.Dst), e.Route[len(e.Route)-1])
	}
}

func assertOnBoundary(t *testing.T, n *Node, p Point) {
	t.Helper()
	const tol = 1e-6
	if math.Abs(p.X-n.X) > n.W/2+tol || math.Abs(p.Y-n.Y) > n.H/2+tol {
		t.Errorf("route endpoint %v outside node %s box", p, n.ID)
	}
}

func TestDoHOLANoEdges(t *testing.T) {
	g := buildGraph(t, []string{"a", "b"}, nil)
	if err := DoHOLA(g); err != nil {
		t.Fatal(err)
	}
}

func TestDoHOLAPureTree(t *testing.T) {
	g := buildGraph(t,
		[]string{"root", "a", "b", "c", "a1", "a2", "b1"},
		[][2]string{
			{"root", "a"}, {"root", "b"}, {"root", "c"},
			{"a", "a1"}, {"a", "a2"}, {"b", "b1"},
		})
	if err := DoHOLA(g); err != nil {
		t.Fatal(err)
	}
	assertLayoutInvariants(t, g)
	// Default growth is South: children sit below their parent.
	if g.Node("a").Y <= g.Node("root").Y {
		t.Errorf("child a (y=%g) not below root (y=%g)", g.Node("a").Y, g.Node("root").Y)
	}
}

func TestDoHOLACycle(t *testing.T) {
	g := buildGraph(t,
		[]string{"a", "b", "c", "d"},
		[][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"}})
	if err := DoHOLA(g); err != nil {
		t.Fatal(err)
	}
	assertLayoutInvariants(t, g)
}

func TestDoHOLACoreWithTrees(t *testing.T) {
	// A 4-cycle core with a pendant chain and a pendant fan.
	g := buildGraph(t,
		[]string{"a", "b", "c", "d", "p", "q", "r", "s", "t"},
		[][2]string{
			{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"},
			{"a", "p"}, {"p", "q"},
			{"c", "r"}, {"c", "s"}, {"s", "t"},
		})
	if err := DoHOLA(g); err != nil {
		t.Fatal(err)
	}
	assertLayoutInvariants(t, g)
}

func TestDoHOLAServiceTopology(t *testing.T) {
	// A shape typical of motel topologies: gateway fanning out to
	// services with shared backing stores.
	g := buildGraph(t,
		[]string{"gateway", "auth", "orders", "catalog", "payments", "db", "cache", "queue", "mailer"},
		[][2]string{
			{"gateway", "auth"}, {"gateway", "orders"}, {"gateway", "catalog"},
			{"orders", "payments"}, {"orders", "db"}, {"catalog", "db"},
			{"catalog", "cache"}, {"auth", "cache"}, {"payments", "queue"},
			{"queue", "mailer"},
		})
	if err := DoHOLA(g); err != nil {
		t.Fatal(err)
	}
	assertLayoutInvariants(t, g)
	if _, _, w, h := g.BoundingBox(); w <= 0 || h <= 0 {
		t.Errorf("degenerate bounding box %gx%g", w, h)
	}
}

func TestDoHOLAPutsUlcAtOrigin(t *testing.T) {
	g := buildGraph(t,
		[]string{"a", "b", "c"},
		[][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}})
	if err := DoHOLA(g); err != nil {
		t.Fatal(err)
	}
	x, y, _, _ := g.BoundingBox()
	const tol = 1e-6
	if math.Abs(x) > tol || math.Abs(y) > tol {
		t.Errorf("bounding box origin = (%g, %g), want (0, 0)", x, y)
	}
}

func TestDoHOLADeterminism(t *testing.T) {
	build := func() *Graph {
		return buildGraph(t,
			[]string{"a", "b", "c", "d", "e", "f", "g"},
			[][2]string{
				{"a", "b"}, {"b", "c"}, {"c", "a"},
				{"c", "d"}, {"d", "e"}, {"b", "f"}, {"f", "g"},
			})
	}
	g1, g2 := build(), build()
	if err := DoHOLA(g1); err != nil {
		t.Fatal(err)
	}
	if err := DoHOLA(g2); err != nil {
		t.Fatal(err)
	}
	for i, n1 := range g1.Nodes() {
		n2 := g2.Nodes()[i]
		if n1.X != n2.X || n1.Y != n2.Y {
			t.Errorf("node %s: (%g, %g) != (%g, %g)", n1.ID, n1.X, n1.Y, n2.X, n2.Y)
		}
	}
	for i, e1 := range g1.Edges() {
		e2 := g2.Edges()[i]
		if len(e1.Route) != len(e2.Route) {
			t.Errorf("edge %s-%s: route lengths differ", e1.Src, e1.Dst)
			continue
		}
		for k := range e1.Route {
			if e1.Route[k] != e2.Route[k] {
				t.Errorf("edge %s-%s: route point %d differs", e1.Src, e1.Dst, k)
			}
		}
	}
}

func TestGraphValidation(t *testing.T) {
	g := NewGraph()
	if _, err := g.AddNode("a", 10, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddNode("a", 10, 10); err == nil {
		t.Error("duplicate node accepted")
	}
	if _, err := g.AddNode("bad", 0, 10); err == nil {
		t.Error("zero-width node accepted")
	}
	if _, err := g.AddEdge("a", "a"); err == nil {
		t.Error("self loop accepted")
	}
	if _, err := g.AddEdge("a", "missing"); err == nil {
		t.Error("edge to unknown node accepted")
	}
	if _, err := g.AddNode("b", 10, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddEdge("a", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddEdge("b", "a"); err == nil {
		t.Error("reversed duplicate edge accepted")
	}
}
