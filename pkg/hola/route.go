package hola

import (
	"container/heap"
	"math"
	"sort"
)

// Orthogonal connector routing, replacing libavoid's router: each edge
// is routed with A* over an orthogonal visibility grid built from node
// box boundaries, with a penalty per bend. Ports are spread along node
// sides so parallel edges do not coincide.

type rect struct{ x1, y1, x2, y2 float64 }

func (r rect) inflate(d float64) rect {
	return rect{r.x1 - d, r.y1 - d, r.x2 + d, r.y2 + d}
}

func nodeRect(lg *layoutGraph, i int) rect {
	return rect{
		lg.x[i] - lg.w[i]/2, lg.y[i] - lg.h[i]/2,
		lg.x[i] + lg.w[i]/2, lg.y[i] + lg.h[i]/2,
	}
}

type portSlot struct{ edge, end int }

type port struct {
	node  int
	side  CompassDir
	point Point
	out   Point // point clearance away from the box, where routing starts
}

type orthoRouter struct {
	lg          *layoutGraph
	rects       []rect
	clearance   float64
	bendPenalty float64
	xs, ys      []float64
}

func newOrthoRouter(lg *layoutGraph, clearance, bendPenalty float64) *orthoRouter {
	r := &orthoRouter{lg: lg, clearance: clearance, bendPenalty: bendPenalty}
	for i := 0; i < lg.n(); i++ {
		r.rects = append(r.rects, nodeRect(lg, i))
	}
	return r
}

// routeAll computes routes for all edges, writing them into the Graph's
// edges, which appear in the same order as lg.edges.
func (r *orthoRouter) routeAll(g *Graph) {
	ports := r.assignPorts()
	for i, e := range r.lg.edges {
		pu, pv := ports[2*i], ports[2*i+1]
		g.edges[i].Route = r.route(e[0], e[1], pu, pv)
	}
}

// assignPorts picks the exit side of each edge endpoint from the
// dominant axis toward the other end, then spreads edges sharing a
// (node, side) along that side.
func (r *orthoRouter) assignPorts() []port {
	lg := r.lg
	ports := make([]port, 2*len(lg.edges))
	bySide := make(map[int]map[CompassDir][]portSlot)
	for i, e := range lg.edges {
		for end := 0; end < 2; end++ {
			u, v := e[end], e[1-end]
			dx := lg.x[v] - lg.x[u]
			dy := lg.y[v] - lg.y[u]
			var side CompassDir
			if math.Abs(dx) >= math.Abs(dy) {
				side = East
				if dx < 0 {
					side = West
				}
			} else {
				side = South
				if dy < 0 {
					side = North
				}
			}
			ports[2*i+end] = port{node: u, side: side}
			if bySide[u] == nil {
				bySide[u] = make(map[CompassDir][]portSlot)
			}
			bySide[u][side] = append(bySide[u][side], portSlot{edge: i, end: end})
		}
	}
	for u, sides := range bySide {
		for side, slots := range sides {
			sort.Slice(slots, func(a, b int) bool {
				oa := otherEndCoord(r.lg, slots[a].edge, slots[a].end, side)
				ob := otherEndCoord(r.lg, slots[b].edge, slots[b].end, side)
				if oa != ob {
					return oa < ob
				}
				return slots[a].edge < slots[b].edge
			})
			r.placePortsOnSide(ports, u, side, slots)
		}
	}
	return ports
}

func otherEndCoord(lg *layoutGraph, edge, end int, side CompassDir) float64 {
	v := lg.edges[edge][1-end]
	if side == East || side == West {
		return lg.y[v]
	}
	return lg.x[v]
}

func (r *orthoRouter) placePortsOnSide(ports []port, u int, side CompassDir, slots []portSlot) {
	lg := r.lg
	box := r.rects[u]
	m := float64(len(slots))
	var sideLen float64
	if side == East || side == West {
		sideLen = lg.h[u]
	} else {
		sideLen = lg.w[u]
	}
	spread := math.Min(2*r.clearance, sideLen/(m+1))
	for k, s := range slots {
		off := (float64(k) - (m-1)/2) * spread
		var p, out Point
		switch side {
		case East:
			p = Point{box.x2, lg.y[u] + off}
			out = Point{box.x2 + r.clearance, p.Y}
		case West:
			p = Point{box.x1, lg.y[u] + off}
			out = Point{box.x1 - r.clearance, p.Y}
		case South:
			p = Point{lg.x[u] + off, box.y2}
			out = Point{p.X, box.y2 + r.clearance}
		default:
			p = Point{lg.x[u] + off, box.y1}
			out = Point{p.X, box.y1 - r.clearance}
		}
		ports[2*s.edge+s.end].point = p
		ports[2*s.edge+s.end].out = out
	}
}

// buildGrid collects the candidate coordinates: box boundaries pushed
// out by the clearance, box centres, and the given extra points.
func (r *orthoRouter) buildGrid(extra []Point) {
	r.xs = r.xs[:0]
	r.ys = r.ys[:0]
	for i, b := range r.rects {
		r.xs = append(r.xs, b.x1-r.clearance, r.lg.x[i], b.x2+r.clearance)
		r.ys = append(r.ys, b.y1-r.clearance, r.lg.y[i], b.y2+r.clearance)
	}
	for _, p := range extra {
		r.xs = append(r.xs, p.X)
		r.ys = append(r.ys, p.Y)
	}
	r.xs = dedupSorted(r.xs)
	r.ys = dedupSorted(r.ys)
}

func dedupSorted(v []float64) []float64 {
	sort.Float64s(v)
	out := v[:0]
	for _, x := range v {
		if len(out) == 0 || x-out[len(out)-1] > overlapEps {
			out = append(out, x)
		}
	}
	return out
}

// route computes an orthogonal route for edge (u,v) between the given
// ports. Falls back to a Z-shaped route when no clear path exists.
func (r *orthoRouter) route(u, v int, pu, pv port) []Point {
	blocked := make([]rect, 0, len(r.rects))
	for i, b := range r.rects {
		if i == u || i == v {
			continue
		}
		blocked = append(blocked, b.inflate(r.clearance/2))
	}
	r.buildGrid([]Point{pu.out, pv.out})

	path := r.astar(pu.out, pv.out, blocked)
	if path == nil {
		path = zRoute(pu.out, pv.out)
	}
	full := append([]Point{pu.point}, path...)
	full = append(full, pv.point)
	return simplifyRoute(full)
}

// zRoute is an unconditional orthogonal fallback between two points.
func zRoute(a, b Point) []Point {
	if a.X == b.X || a.Y == b.Y {
		return []Point{a, b}
	}
	midX := (a.X + b.X) / 2
	return []Point{a, {midX, a.Y}, {midX, b.Y}, b}
}

type pqItem struct {
	state    gridState
	priority float64
	index    int
}

type gridState struct {
	ix, iy int
	horiz  bool
	start  bool
}

type pq []*pqItem

func (q pq) Len() int { return len(q) }
func (q pq) Less(i, j int) bool {
	if q[i].priority != q[j].priority {
		return q[i].priority < q[j].priority
	}
	a, b := q[i].state, q[j].state
	if a.ix != b.ix {
		return a.ix < b.ix
	}
	if a.iy != b.iy {
		return a.iy < b.iy
	}
	return !a.horiz && b.horiz
}
func (q pq) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index, q[j].index = i, j
}
func (q *pq) Push(x any) {
	it := x.(*pqItem)
	it.index = len(*q)
	*q = append(*q, it)
}
func (q *pq) Pop() any {
	old := *q
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*q = old[:n-1]
	return it
}

func (r *orthoRouter) astar(from, to Point, blocked []rect) []Point {
	fx := indexOf(r.xs, from.X)
	fy := indexOf(r.ys, from.Y)
	tx := indexOf(r.xs, to.X)
	ty := indexOf(r.ys, to.Y)
	if fx < 0 || fy < 0 || tx < 0 || ty < 0 {
		return nil
	}
	start := gridState{ix: fx, iy: fy, start: true}
	dist := map[gridState]float64{start: 0}
	parent := map[gridState]gridState{}
	h := func(s gridState) float64 {
		return math.Abs(r.xs[s.ix]-r.xs[tx]) + math.Abs(r.ys[s.iy]-r.ys[ty])
	}
	open := &pq{{state: start, priority: h(start)}}
	heap.Init(open)
	closed := map[gridState]bool{}

	for open.Len() > 0 {
		cur := heap.Pop(open).(*pqItem).state
		if closed[cur] {
			continue
		}
		closed[cur] = true
		if cur.ix == tx && cur.iy == ty {
			return r.reconstruct(parent, cur, start)
		}
		for _, nb := range r.neighbours(cur, blocked) {
			step := math.Abs(r.xs[nb.ix]-r.xs[cur.ix]) + math.Abs(r.ys[nb.iy]-r.ys[cur.iy])
			cost := dist[cur] + step
			if !cur.start && cur.horiz != nb.horiz {
				cost += r.bendPenalty
			}
			if d, ok := dist[nb]; !ok || cost < d-overlapEps {
				dist[nb] = cost
				parent[nb] = cur
				heap.Push(open, &pqItem{state: nb, priority: cost + h(nb)})
			}
		}
	}
	return nil
}

func (r *orthoRouter) neighbours(s gridState, blocked []rect) []gridState {
	var out []gridState
	if s.ix > 0 && !r.segBlocked(r.xs[s.ix-1], r.xs[s.ix], r.ys[s.iy], true, blocked) {
		out = append(out, gridState{ix: s.ix - 1, iy: s.iy, horiz: true})
	}
	if s.ix < len(r.xs)-1 && !r.segBlocked(r.xs[s.ix], r.xs[s.ix+1], r.ys[s.iy], true, blocked) {
		out = append(out, gridState{ix: s.ix + 1, iy: s.iy, horiz: true})
	}
	if s.iy > 0 && !r.segBlocked(r.ys[s.iy-1], r.ys[s.iy], r.xs[s.ix], false, blocked) {
		out = append(out, gridState{ix: s.ix, iy: s.iy - 1})
	}
	if s.iy < len(r.ys)-1 && !r.segBlocked(r.ys[s.iy], r.ys[s.iy+1], r.xs[s.ix], false, blocked) {
		out = append(out, gridState{ix: s.ix, iy: s.iy + 1})
	}
	return out
}

// segBlocked reports whether an axis-aligned segment from lo to hi at
// fixed cross-coordinate c intersects any blocked rectangle interior.
func (r *orthoRouter) segBlocked(lo, hi, c float64, horizontal bool, blocked []rect) bool {
	for _, b := range blocked {
		if horizontal {
			if c > b.y1 && c < b.y2 && hi > b.x1 && lo < b.x2 {
				return true
			}
		} else {
			if c > b.x1 && c < b.x2 && hi > b.y1 && lo < b.y2 {
				return true
			}
		}
	}
	return false
}

func (r *orthoRouter) reconstruct(parent map[gridState]gridState, cur, start gridState) []Point {
	var rev []Point
	for {
		rev = append(rev, Point{r.xs[cur.ix], r.ys[cur.iy]})
		if cur == start {
			break
		}
		cur = parent[cur]
	}
	out := make([]Point, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out
}

func indexOf(v []float64, x float64) int {
	i := sort.SearchFloat64s(v, x-overlapEps)
	if i < len(v) && math.Abs(v[i]-x) <= overlapEps {
		return i
	}
	return -1
}

// simplifyRoute removes duplicate and collinear points, and inserts a
// bend point wherever consecutive points are not axis-aligned (which
// can only arise from port offsets meeting the fallback route).
func simplifyRoute(pts []Point) []Point {
	var clean []Point
	for _, p := range pts {
		if len(clean) > 0 {
			last := clean[len(clean)-1]
			if math.Abs(last.X-p.X) <= overlapEps && math.Abs(last.Y-p.Y) <= overlapEps {
				continue
			}
			if math.Abs(last.X-p.X) > overlapEps && math.Abs(last.Y-p.Y) > overlapEps {
				clean = append(clean, Point{last.X, p.Y})
			}
		}
		clean = append(clean, p)
	}
	if len(clean) < 3 {
		return clean
	}
	out := []Point{clean[0]}
	for i := 1; i < len(clean)-1; i++ {
		a, b, c := out[len(out)-1], clean[i], clean[i+1]
		collinear := (math.Abs(a.X-b.X) <= overlapEps && math.Abs(b.X-c.X) <= overlapEps) ||
			(math.Abs(a.Y-b.Y) <= overlapEps && math.Abs(b.Y-c.Y) <= overlapEps)
		if !collinear {
			out = append(out, b)
		}
	}
	out = append(out, clean[len(clean)-1])
	return out
}
