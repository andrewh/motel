// Package hola is a Go port of the HOLA (Human-like Orthogonal Layout
// Algorithm) engine from the adaptagrams libdialect library.
//
// The port follows the pipeline of libdialect's doHOLA (hola.cpp):
// peel trees from the core, lay the core out with constrained stress
// minimisation ("destress"), orthogonalise hubs and links with alignment
// constraints, reattach symmetrically laid out trees, then route edges
// orthogonally. Constraint projection is a port of the libvpsc solver.
//
// Two stages are simplified relative to the C++ original: the
// planarisation/faces machinery is replaced by direct tree reattachment
// with rigid clusters and VPSC-driven expansion, and libavoid's router is
// replaced by an A* search over an orthogonal visibility grid.
package hola

import (
	"fmt"
	"math"
	"sort"
)

// Point is a position in the layout plane. Y grows downward.
type Point struct {
	X, Y float64
}

// Node is a rectangular node identified by a caller-supplied ID.
// X and Y give the centre of the rectangle.
type Node struct {
	ID   string
	X, Y float64
	W, H float64

	padW, padH float64
}

// Edge connects two nodes by ID. After layout, Route holds an orthogonal
// polyline from a point on the source boundary to one on the target
// boundary.
type Edge struct {
	Src, Dst string
	Route    []Point
}

// Graph is the input and output of DoHOLA. Nodes and edges keep their
// identity through layout; DoHOLA sets node positions and edge routes.
type Graph struct {
	nodes  map[string]*Node
	order  []string
	edges  []*Edge
	edgeAt map[[2]string]*Edge
}

// NewGraph returns an empty graph.
func NewGraph() *Graph {
	return &Graph{
		nodes:  make(map[string]*Node),
		edgeAt: make(map[[2]string]*Edge),
	}
}

// AddNode adds a node with the given size. Adding a duplicate ID or a
// non-positive size is an error.
func (g *Graph) AddNode(id string, w, h float64) (*Node, error) {
	if _, ok := g.nodes[id]; ok {
		return nil, fmt.Errorf("hola: %w: %q", ErrDuplicateNode, id)
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("hola: %w: node %q has size %gx%g", ErrInvalidSize, id, w, h)
	}
	n := &Node{ID: id, W: w, H: h}
	g.nodes[id] = n
	g.order = append(g.order, id)
	return n, nil
}

// AddEdge adds an edge between two existing nodes. Self-loops and
// duplicate edges (in either direction) are rejected, matching the
// simple-graph model HOLA operates on.
func (g *Graph) AddEdge(src, dst string) (*Edge, error) {
	if src == dst {
		return nil, fmt.Errorf("hola: %w: %q", ErrSelfLoop, src)
	}
	for _, id := range []string{src, dst} {
		if _, ok := g.nodes[id]; !ok {
			return nil, fmt.Errorf("hola: %w: %q", ErrUnknownNode, id)
		}
	}
	if _, ok := g.edgeAt[[2]string{src, dst}]; ok {
		return nil, fmt.Errorf("hola: %w: %s-%s", ErrDuplicateEdge, src, dst)
	}
	if _, ok := g.edgeAt[[2]string{dst, src}]; ok {
		return nil, fmt.Errorf("hola: %w: %s-%s", ErrDuplicateEdge, src, dst)
	}
	e := &Edge{Src: src, Dst: dst}
	g.edges = append(g.edges, e)
	g.edgeAt[[2]string{src, dst}] = e
	return e, nil
}

// Node returns the node with the given ID, or nil.
func (g *Graph) Node(id string) *Node { return g.nodes[id] }

// Nodes returns the nodes in insertion order.
func (g *Graph) Nodes() []*Node {
	out := make([]*Node, len(g.order))
	for i, id := range g.order {
		out[i] = g.nodes[id]
	}
	return out
}

// Edges returns the edges in insertion order.
func (g *Graph) Edges() []*Edge { return g.edges }

// NumNodes returns the node count.
func (g *Graph) NumNodes() int { return len(g.nodes) }

// NumEdges returns the edge count.
func (g *Graph) NumEdges() int { return len(g.edges) }

// BoundingBox returns the smallest axis-aligned rectangle containing all
// node boxes and route points. It returns the upper-left corner and size.
func (g *Graph) BoundingBox() (x, y, w, h float64) {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, id := range g.order {
		n := g.nodes[id]
		minX = math.Min(minX, n.X-n.W/2)
		maxX = math.Max(maxX, n.X+n.W/2)
		minY = math.Min(minY, n.Y-n.H/2)
		maxY = math.Max(maxY, n.Y+n.H/2)
	}
	for _, e := range g.edges {
		for _, p := range e.Route {
			minX = math.Min(minX, p.X)
			maxX = math.Max(maxX, p.X)
			minY = math.Min(minY, p.Y)
			maxY = math.Max(maxY, p.Y)
		}
	}
	if math.IsInf(minX, 1) {
		return 0, 0, 0, 0
	}
	return minX, minY, maxX - minX, maxY - minY
}

// iel returns the inferred ideal edge length: twice the average node
// dimension, as in libdialect's Graph::autoInferIEL.
func (g *Graph) iel() float64 {
	if len(g.nodes) == 0 {
		return defaultIEL
	}
	var sum float64
	for _, id := range g.order {
		n := g.nodes[id]
		sum += (n.W + n.H) / 2
	}
	avg := sum / float64(len(g.nodes))
	if avg <= 0 {
		return defaultIEL
	}
	return 2 * avg
}

const defaultIEL = 80.0

// layoutGraph is the indexed working representation used by the layout
// stages. Node identity is by index; ids maps back to Graph node IDs.
type layoutGraph struct {
	ids   []string
	index map[string]int
	x, y  []float64
	w, h  []float64
	adj   [][]int
	edges [][2]int
}

func newLayoutGraph(g *Graph) *layoutGraph {
	ids := make([]string, len(g.order))
	copy(ids, g.order)
	sort.Strings(ids)
	lg := &layoutGraph{
		ids:   ids,
		index: make(map[string]int, len(ids)),
		x:     make([]float64, len(ids)),
		y:     make([]float64, len(ids)),
		w:     make([]float64, len(ids)),
		h:     make([]float64, len(ids)),
		adj:   make([][]int, len(ids)),
	}
	for i, id := range ids {
		lg.index[id] = i
		n := g.nodes[id]
		lg.x[i], lg.y[i] = n.X, n.Y
		lg.w[i], lg.h[i] = n.W, n.H
	}
	for _, e := range g.edges {
		u, v := lg.index[e.Src], lg.index[e.Dst]
		lg.edges = append(lg.edges, [2]int{u, v})
		lg.adj[u] = append(lg.adj[u], v)
		lg.adj[v] = append(lg.adj[v], u)
	}
	for i := range lg.adj {
		sort.Ints(lg.adj[i])
	}
	return lg
}

func (lg *layoutGraph) n() int { return len(lg.ids) }

func (lg *layoutGraph) degree(i int) int { return len(lg.adj[i]) }

// pad grows (or, when negative, shrinks) every node box symmetrically.
func (lg *layoutGraph) pad(dw, dh float64) {
	for i := range lg.w {
		lg.w[i] += dw
		lg.h[i] += dh
	}
}

// shortestPathLengths returns BFS hop counts from src; unreachable nodes
// get -1.
func (lg *layoutGraph) shortestPathLengths(src int) []int {
	dist := make([]int, lg.n())
	for i := range dist {
		dist[i] = -1
	}
	dist[src] = 0
	queue := []int{src}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range lg.adj[u] {
			if dist[v] < 0 {
				dist[v] = dist[u] + 1
				queue = append(queue, v)
			}
		}
	}
	return dist
}

func overlap1D(c1, s1, c2, s2 float64) float64 {
	return (s1+s2)/2 - math.Abs(c1-c2)
}

// rectsOverlap reports whether node boxes i and j overlap in both
// dimensions by more than eps.
func (lg *layoutGraph) rectsOverlap(i, j int, eps float64) bool {
	return overlap1D(lg.x[i], lg.w[i], lg.x[j], lg.w[j]) > eps &&
		overlap1D(lg.y[i], lg.h[i], lg.y[j], lg.h[j]) > eps
}
