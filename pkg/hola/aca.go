package hola

import (
	"math"
	"sort"
)

// Orthogonalisation: orthoHubLayout assigns the incident edges of
// high-degree nodes ("hubs") to compass directions and constrains them
// accordingly (the analogue of OrthoHubLayout), and acaCreateAlignments
// aligns the remaining links greedily (the analogue of
// ACALayout::createAlignments).

const hubMinDegree = 3

// edgeKey returns a canonical pair for an undirected edge.
func edgeKey(u, v int) [2]int {
	if u > v {
		u, v = v, u
	}
	return [2]int{u, v}
}

// dirAssignment tracks which cardinal directions are already used by
// aligned edges at each node, to prevent coincident edges.
type dirAssignment map[int]map[CompassDir]bool

func (da dirAssignment) taken(node int, dir CompassDir) bool {
	return da[node][dir]
}

func (da dirAssignment) take(node int, dir CompassDir) {
	if da[node] == nil {
		da[node] = make(map[CompassDir]bool)
	}
	da[node][dir] = true
}

// edgeDir returns the cardinal direction from u toward v implied by an
// alignment in dimension d (d is the dimension in which the two nodes
// are separated, the perpendicular one being equalised).
func edgeDir(lg *layoutGraph, u, v int, d dim) CompassDir {
	if d == dimX {
		if lg.x[v] >= lg.x[u] {
			return East
		}
		return West
	}
	if lg.y[v] >= lg.y[u] {
		return South
	}
	return North
}

// alignEdge adds the constraints making edge (u,v) orthogonal: an
// equality in the perpendicular dimension and a separation keeping the
// boxes apart in the aligned dimension.
func alignEdge(st *layoutState, u, v int, d dim, da dirAssignment, aligned map[[2]int]dim) {
	lg := st.lg
	var gap float64
	if d == dimX {
		gap = (lg.w[u] + lg.w[v]) / 2
		st.addAlignment(dimY, u, v)
		st.cons = append(st.cons, orderedSep(dimX, u, v, lg.x, gap))
	} else {
		gap = (lg.h[u] + lg.h[v]) / 2
		st.addAlignment(dimX, u, v)
		st.cons = append(st.cons, orderedSep(dimY, u, v, lg.y, gap))
	}
	da.take(u, edgeDir(lg, u, v, d))
	da.take(v, edgeDir(lg, v, u, d))
	aligned[edgeKey(u, v)] = d
}

// orthoHubLayout processes hubs in descending degree order, assigning
// each incident edge to the nearest free cardinal direction by angle.
func orthoHubLayout(st *layoutState, da dirAssignment, aligned map[[2]int]dim) {
	lg := st.lg
	var hubs []int
	for i := 0; i < lg.n(); i++ {
		if lg.degree(i) >= hubMinDegree {
			hubs = append(hubs, i)
		}
	}
	sort.Slice(hubs, func(a, b int) bool {
		if lg.degree(hubs[a]) != lg.degree(hubs[b]) {
			return lg.degree(hubs[a]) > lg.degree(hubs[b])
		}
		return hubs[a] < hubs[b]
	})

	for _, u := range hubs {
		type cand struct {
			v    int
			dir  CompassDir
			cost float64
		}
		var cands []cand
		for _, v := range lg.adj[u] {
			if _, done := aligned[edgeKey(u, v)]; done {
				continue
			}
			theta := math.Atan2(lg.y[v]-lg.y[u], lg.x[v]-lg.x[u])
			for _, dc := range []struct {
				dir   CompassDir
				angle float64
			}{{East, 0}, {South, math.Pi / 2}, {West, math.Pi}, {North, -math.Pi / 2}} {
				diff := math.Abs(angleDiff(theta, dc.angle))
				cands = append(cands, cand{v: v, dir: dc.dir, cost: diff})
			}
		}
		sort.Slice(cands, func(a, b int) bool {
			if cands[a].cost != cands[b].cost {
				return cands[a].cost < cands[b].cost
			}
			if cands[a].v != cands[b].v {
				return cands[a].v < cands[b].v
			}
			return cands[a].dir < cands[b].dir
		})
		assignedNbr := make(map[int]bool)
		for _, c := range cands {
			if assignedNbr[c.v] || da.taken(u, c.dir) {
				continue
			}
			if _, done := aligned[edgeKey(u, c.v)]; done {
				continue
			}
			rev := c.dir.rotateCW(2)
			if da.taken(c.v, rev) {
				continue
			}
			d := dimY
			if c.dir == East || c.dir == West {
				d = dimX
			}
			if !dirMatchesCurrentOrder(st.lg, u, c.v, c.dir) {
				continue
			}
			alignEdge(st, u, c.v, d, da, aligned)
			assignedNbr[c.v] = true
		}
	}
}

// dirMatchesCurrentOrder checks that aligning u->v in dir agrees with
// the nodes' current relative order, so the separation constraint added
// by alignEdge does not fight the alignment.
func dirMatchesCurrentOrder(lg *layoutGraph, u, v int, dir CompassDir) bool {
	switch dir {
	case East:
		return lg.x[v] >= lg.x[u]
	case West:
		return lg.x[v] <= lg.x[u]
	case South:
		return lg.y[v] >= lg.y[u]
	default:
		return lg.y[v] <= lg.y[u]
	}
}

func angleDiff(a, b float64) float64 {
	d := math.Mod(a-b, 2*math.Pi)
	if d > math.Pi {
		d -= 2 * math.Pi
	}
	if d < -math.Pi {
		d += 2 * math.Pi
	}
	return d
}

// acaCreateAlignments greedily aligns remaining edges, cheapest
// deviation first, skipping any alignment that would create coincident
// edges at a shared node.
func acaCreateAlignments(st *layoutState, da dirAssignment, aligned map[[2]int]dim) {
	lg := st.lg
	type cand struct {
		u, v int
		d    dim
		cost float64
	}
	for {
		var cands []cand
		for _, e := range lg.edges {
			u, v := e[0], e[1]
			if _, done := aligned[edgeKey(u, v)]; done {
				continue
			}
			dx := math.Abs(lg.x[v] - lg.x[u])
			dy := math.Abs(lg.y[v] - lg.y[u])
			if dx >= dy {
				cands = append(cands, cand{u: u, v: v, d: dimX, cost: dy})
			} else {
				cands = append(cands, cand{u: u, v: v, d: dimY, cost: dx})
			}
		}
		sort.Slice(cands, func(a, b int) bool {
			if cands[a].cost != cands[b].cost {
				return cands[a].cost < cands[b].cost
			}
			if cands[a].u != cands[b].u {
				return cands[a].u < cands[b].u
			}
			return cands[a].v < cands[b].v
		})
		applied := false
		for _, c := range cands {
			du := edgeDir(lg, c.u, c.v, c.d)
			dv := edgeDir(lg, c.v, c.u, c.d)
			if da.taken(c.u, du) || da.taken(c.v, dv) {
				continue
			}
			alignEdge(st, c.u, c.v, c.d, da, aligned)
			applied = true
			break
		}
		if !applied {
			return
		}
		// Re-project after each alignment so later candidates are
		// evaluated against updated positions, as in ACA's loop.
		st.project(dimX, colaOpts{}, append([]float64(nil), st.lg.x...))
		st.project(dimY, colaOpts{}, append([]float64(nil), st.lg.y...))
	}
}

// nearAlignments snaps almost-aligned edges, the lightweight analogue
// of doNearAlignments.
func nearAlignments(st *layoutState, aligned map[[2]int]dim, da dirAssignment, kinkWidth float64) {
	lg := st.lg
	for _, e := range lg.edges {
		u, v := e[0], e[1]
		if _, done := aligned[edgeKey(u, v)]; done {
			continue
		}
		dx := math.Abs(lg.x[v] - lg.x[u])
		dy := math.Abs(lg.y[v] - lg.y[u])
		var d dim
		switch {
		case dy <= kinkWidth && dy <= dx:
			d = dimX
		case dx <= kinkWidth && dx < dy:
			d = dimY
		default:
			continue
		}
		du := edgeDir(lg, u, v, d)
		dv := edgeDir(lg, v, u, d)
		if da.taken(u, du) || da.taken(v, dv) {
			continue
		}
		alignEdge(st, u, v, d, da, aligned)
	}
}
