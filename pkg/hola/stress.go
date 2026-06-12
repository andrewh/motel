package hola

import "math"

// layoutState carries the working graph, the inferred ideal edge length
// and the persistent constraint set (the analogue of libdialect's
// SepMatrix) through the pipeline stages.
type layoutState struct {
	lg     *layoutGraph
	iel    float64
	cons   []sepConstraint
	weight []float64
}

func newLayoutState(lg *layoutGraph, iel float64) *layoutState {
	w := make([]float64, lg.n())
	for i := range w {
		w[i] = 1
	}
	return &layoutState{lg: lg, iel: iel, weight: w}
}

// colaOpts mirrors the ColaOptions fields of libdialect that this port
// supports.
type colaOpts struct {
	preventOverlaps    bool
	solidAlignedEdges  bool
	useNeighbourStress bool
	extraGap           float64
}

const (
	stressMaxIters       = 60
	stressTolScalar      = 1e-3
	minSeparationDist    = 1e-9
	disconnectedDistHops = 1.0 // extra hops per node for disconnected pairs
	solidEdgeHalfWidth   = 2.0
	overlapEps           = 1e-7
)

// destress performs constrained stress majorisation: alternating
// majorisation steps with VPSC projection onto the constraint set, the
// port of Graph::destress.
func (st *layoutState) destress(o colaOpts) {
	n := st.lg.n()
	if n <= 1 {
		return
	}
	D := st.idealDistances(o.useNeighbourStress)
	tol := stressTolScalar * st.iel
	for iter := 0; iter < stressMaxIters; iter++ {
		prevX := append([]float64(nil), st.lg.x...)
		prevY := append([]float64(nil), st.lg.y...)
		dx := st.majorize(D, dimX)
		st.project(dimX, o, dx)
		dy := st.majorize(D, dimY)
		st.project(dimY, o, dy)
		if st.maxMove(prevX, prevY) < tol {
			break
		}
	}
}

const repairMaxPasses = 10

// removeOverlaps performs pure feasibility passes: projections from the
// current positions with overlap prevention, repeated until no node
// boxes overlap. Unlike destress there is no stress term pulling nodes
// back together, so this converges to a feasible configuration.
func (st *layoutState) removeOverlaps(o colaOpts) {
	o.preventOverlaps = true
	for pass := 0; pass < repairMaxPasses; pass++ {
		if !st.hasOverlaps() {
			return
		}
		st.project(dimX, o, append([]float64(nil), st.lg.x...))
		st.project(dimY, o, append([]float64(nil), st.lg.y...))
	}
}

func (st *layoutState) hasOverlaps() bool {
	for i := 0; i < st.lg.n(); i++ {
		for j := i + 1; j < st.lg.n(); j++ {
			if st.lg.rectsOverlap(i, j, overlapEps) {
				return true
			}
		}
	}
	return false
}

func (st *layoutState) maxMove(px, py []float64) float64 {
	var m float64
	for i := range px {
		m = math.Max(m, math.Hypot(st.lg.x[i]-px[i], st.lg.y[i]-py[i]))
	}
	return m
}

// idealDistances returns the matrix of ideal pairwise distances: BFS hop
// count times the ideal edge length. Disconnected pairs get a large
// finite distance so components stay loosely together. When
// neighbourStress is set, only adjacent pairs attract.
func (st *layoutState) idealDistances(neighbourStress bool) [][]float64 {
	n := st.lg.n()
	D := make([][]float64, n)
	far := st.iel * (float64(n)*disconnectedDistHops + 1)
	for i := 0; i < n; i++ {
		D[i] = make([]float64, n)
		hops := st.lg.shortestPathLengths(i)
		for j := 0; j < n; j++ {
			switch {
			case i == j:
				D[i][j] = 0
			case hops[j] < 0:
				D[i][j] = far
			case neighbourStress && hops[j] > 1:
				D[i][j] = 0 // excluded from the stress sum
			default:
				D[i][j] = float64(hops[j]) * st.iel
			}
		}
	}
	return D
}

// majorize computes the unconstrained majorisation update for one
// dimension from the current positions.
func (st *layoutState) majorize(D [][]float64, d dim) []float64 {
	n := st.lg.n()
	cur := st.lg.x
	if d == dimY {
		cur = st.lg.y
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		var num, den float64
		for j := 0; j < n; j++ {
			if j == i || D[i][j] <= 0 {
				continue
			}
			dij := D[i][j]
			w := 1 / (dij * dij)
			dx := st.lg.x[i] - st.lg.x[j]
			dy := st.lg.y[i] - st.lg.y[j]
			dist := math.Hypot(dx, dy)
			var dir float64
			if dist < minSeparationDist {
				// Deterministic tie-break for coincident nodes.
				dir = float64((i-j)%5) * minSeparationDist
				dist = minSeparationDist
			} else if d == dimX {
				dir = dx
			} else {
				dir = dy
			}
			num += w * (cur[j] + dij*dir/dist)
			den += w
		}
		if den > 0 {
			out[i] = num / den
		} else {
			out[i] = cur[i]
		}
	}
	return out
}

// project moves nodes in dimension d as close as possible to the desired
// positions subject to the persistent constraints, plus generated
// non-overlap and solid-edge constraints when requested. The port of
// Graph::project.
func (st *layoutState) project(d dim, o colaOpts, desired []float64) {
	cons := st.consForDim(d)
	if o.preventOverlaps {
		cons = append(cons, st.generateOverlapConstraints(d, o.extraGap)...)
	}
	if o.solidAlignedEdges {
		cons = append(cons, st.generateSolidEdgeConstraints(d, o.extraGap)...)
	}
	pos := solveVPSC(desired, st.weight, cons)
	if d == dimX {
		copy(st.lg.x, pos)
	} else {
		copy(st.lg.y, pos)
	}
}

func (st *layoutState) consForDim(d dim) []sepConstraint {
	out := make([]sepConstraint, 0, len(st.cons))
	for _, c := range st.cons {
		if c.d == d {
			out = append(out, c)
		}
	}
	return out
}

// generateOverlapConstraints creates separation constraints in dimension
// d between pairs of nodes whose extents overlap in the other dimension.
// Pairs overlapping in both dimensions are resolved in the dimension
// where the overlap is smaller, unless an equality alignment makes that
// dimension unable to separate them. Pairs already separated in d get an
// (already satisfied) constraint maintaining their order, so projection
// cannot push previously separated nodes into overlap.
func (st *layoutState) generateOverlapConstraints(d dim, extraGap float64) []sepConstraint {
	lg := st.lg
	sameD := st.equalityClasses(d)
	sameOther := st.equalityClasses(d.other())
	var out []sepConstraint
	for i := 0; i < lg.n(); i++ {
		for j := i + 1; j < lg.n(); j++ {
			ox := overlap1D(lg.x[i], lg.w[i]+extraGap, lg.x[j], lg.w[j]+extraGap)
			oy := overlap1D(lg.y[i], lg.h[i]+extraGap, lg.y[j], lg.h[j]+extraGap)
			oThis, oOther := ox, oy
			if d == dimY {
				oThis, oOther = oy, ox
			}
			if oOther <= overlapEps {
				continue
			}
			if sameD.find(i) == sameD.find(j) {
				continue
			}
			if oThis > overlapEps {
				// True overlap: leave it to the dimension with the
				// smaller overlap, when that dimension can separate.
				otherCan := sameOther.find(i) != sameOther.find(j)
				if d == dimX && ox > oy && otherCan {
					continue
				}
				if d == dimY && ox <= oy && otherCan {
					continue
				}
			}
			var c sepConstraint
			if d == dimX {
				c = orderedSep(dimX, i, j, lg.x, (lg.w[i]+lg.w[j])/2+extraGap)
			} else {
				c = orderedSep(dimY, i, j, lg.y, (lg.h[i]+lg.h[j])/2+extraGap)
			}
			out = append(out, c)
		}
	}
	return out
}

// unionFind tracks the equivalence classes induced by equality
// constraints in one dimension; nodes in the same class cannot be
// separated in that dimension.
type unionFind []int

func newUnionFind(n int) unionFind {
	uf := make(unionFind, n)
	for i := range uf {
		uf[i] = i
	}
	return uf
}

func (uf unionFind) find(i int) int {
	for uf[i] != i {
		uf[i] = uf[uf[i]]
		i = uf[i]
	}
	return i
}

func (uf unionFind) union(i, j int) {
	uf[uf.find(i)] = uf.find(j)
}

func (st *layoutState) equalityClasses(d dim) unionFind {
	uf := newUnionFind(st.lg.n())
	for _, c := range st.cons {
		if c.d == d && c.equality {
			uf.union(c.left, c.right)
		}
	}
	return uf
}

// orderedSep builds a separation constraint between i and j preserving
// their current order in the given coordinate.
func orderedSep(d dim, i, j int, coord []float64, gap float64) sepConstraint {
	if coord[i] <= coord[j] {
		return sepConstraint{d: d, left: i, right: j, gap: gap}
	}
	return sepConstraint{d: d, left: j, right: i, gap: gap}
}

// generateSolidEdgeConstraints keeps nodes from overlapping aligned
// edges, the analogue of ColaOptions::solidifyAlignedEdges. An edge
// aligned in dimension d behaves as a thin rectangle spanning between
// its endpoints in the other dimension.
func (st *layoutState) generateSolidEdgeConstraints(d dim, extraGap float64) []sepConstraint {
	lg := st.lg
	var out []sepConstraint
	for _, e := range lg.edges {
		u, v := e[0], e[1]
		if !st.aligned(d, u, v) {
			continue
		}
		// The edge runs along the other dimension at a fixed coordinate
		// in d; nodes whose span crosses it must keep clear in d.
		lo, hi := spanAlong(d.other(), lg, u, v)
		for k := 0; k < lg.n(); k++ {
			if k == u || k == v {
				continue
			}
			kc, ks := coordAndSize(d.other(), lg, k)
			if kc+ks/2 <= lo || kc-ks/2 >= hi {
				continue
			}
			_, kw := coordAndSize(d, lg, k)
			gap := kw/2 + solidEdgeHalfWidth + extraGap/2
			ec, _ := coordAndSize(d, lg, u)
			kd, _ := coordAndSize(d, lg, k)
			if math.Abs(kd-ec) >= gap {
				continue
			}
			if kd <= ec {
				out = append(out,
					sepConstraint{d: d, left: k, right: u, gap: gap},
					sepConstraint{d: d, left: k, right: v, gap: gap})
			} else {
				out = append(out,
					sepConstraint{d: d, left: u, right: k, gap: gap},
					sepConstraint{d: d, left: v, right: k, gap: gap})
			}
		}
	}
	return out
}

// aligned reports whether nodes u and v are bound to the same coordinate
// in dimension d. Equality constraints with non-zero gaps (rigid tree
// offsets) are not alignments.
func (st *layoutState) aligned(d dim, u, v int) bool {
	for _, c := range st.cons {
		if c.d == d && c.equality && c.gap == 0 &&
			((c.left == u && c.right == v) || (c.left == v && c.right == u)) {
			return true
		}
	}
	return false
}

// addAlignment records an equality constraint placing u and v at the
// same coordinate in dimension d.
func (st *layoutState) addAlignment(d dim, u, v int) {
	if !st.aligned(d, u, v) {
		st.cons = append(st.cons, sepConstraint{d: d, left: u, right: v, equality: true})
	}
}

func coordAndSize(d dim, lg *layoutGraph, i int) (float64, float64) {
	if d == dimX {
		return lg.x[i], lg.w[i]
	}
	return lg.y[i], lg.h[i]
}

func spanAlong(d dim, lg *layoutGraph, u, v int) (float64, float64) {
	cu, _ := coordAndSize(d, lg, u)
	cv, _ := coordAndSize(d, lg, v)
	return math.Min(cu, cv), math.Max(cu, cv)
}
