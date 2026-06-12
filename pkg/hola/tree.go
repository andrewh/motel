package hola

import (
	"math"
	"sort"
)

// CompassDir is a cardinal direction in the layout plane (Y grows
// downward, so South points down the page).
type CompassDir int

// Cardinal directions.
const (
	North CompassDir = iota
	East
	South
	West
)

func (d CompassDir) rotateCW(quarterTurns int) CompassDir {
	return CompassDir((int(d) + quarterTurns) % 4)
}

// symmetricLayout computes positions for the tree's nodes relative to
// its root at the origin, growing in the given direction. It is the
// analogue of Tree::symmetricLayout: subtrees are laid out recursively
// and arranged side by side; with preferConvex the widest subtrees are
// placed centrally so each rank has a convex breadth profile.
func (t *peeledTree) symmetricLayout(lg *layoutGraph, growth CompassDir, nodeSep, rankSep float64, preferConvex bool) {
	t.growth = growth
	t.relX = make(map[int]float64, len(t.nodes))
	t.relY = make(map[int]float64, len(t.nodes))

	// breadth/depth extents of each node in the growth frame.
	breadth := func(i int) float64 {
		if growth == North || growth == South {
			return lg.w[i]
		}
		return lg.h[i]
	}
	depthSize := func(i int) float64 {
		if growth == North || growth == South {
			return lg.h[i]
		}
		return lg.w[i]
	}

	// Per-rank depth coordinates, accumulated from the maximal node
	// depth extent in each rank.
	rank := map[int]int{t.root: 0}
	order := []int{t.root}
	for qi := 0; qi < len(order); qi++ {
		u := order[qi]
		for _, c := range t.children[u] {
			rank[c] = rank[u] + 1
			order = append(order, c)
		}
	}
	maxRank := 0
	for _, r := range rank {
		maxRank = max(maxRank, r)
	}
	rankExtent := make([]float64, maxRank+1)
	for _, v := range t.nodes {
		rankExtent[rank[v]] = math.Max(rankExtent[rank[v]], depthSize(v))
	}
	rankDepth := make([]float64, maxRank+1)
	for r := 1; r <= maxRank; r++ {
		rankDepth[r] = rankDepth[r-1] + (rankExtent[r-1]+rankExtent[r])/2 + rankSep
	}

	// breadthPos holds each node's coordinate along the breadth axis.
	breadthPos := make(map[int]float64, len(t.nodes))
	var widthOf func(v int) float64
	widths := make(map[int]float64, len(t.nodes))
	widthOf = func(v int) float64 {
		var span float64
		for i, c := range t.children[v] {
			if i > 0 {
				span += nodeSep
			}
			span += widthOf(c)
		}
		w := math.Max(breadth(v), span)
		widths[v] = w
		return w
	}
	widthOf(t.root)

	var place func(v int, centre float64)
	place = func(v int, centre float64) {
		breadthPos[v] = centre
		kids := t.children[v]
		if len(kids) == 0 {
			return
		}
		kids = arrangeKids(kids, widths, preferConvex)
		var span float64
		for i, c := range kids {
			if i > 0 {
				span += nodeSep
			}
			span += widths[c]
		}
		pos := centre - span/2
		for _, c := range kids {
			place(c, pos+widths[c]/2)
			pos += widths[c] + nodeSep
		}
	}
	place(t.root, 0)

	for _, v := range t.nodes {
		a, b := breadthPos[v], rankDepth[rank[v]]
		switch growth {
		case South:
			t.relX[v], t.relY[v] = a, b
		case North:
			t.relX[v], t.relY[v] = a, -b
		case East:
			t.relX[v], t.relY[v] = b, a
		case West:
			t.relX[v], t.relY[v] = -b, a
		}
	}
}

// arrangeKids orders subtrees for placement. With preferConvex, the
// widest subtrees go to the middle, producing a symmetric convex
// profile; otherwise the natural (index) order is kept.
func arrangeKids(kids []int, widths map[int]float64, preferConvex bool) []int {
	if !preferConvex || len(kids) < 3 {
		return kids
	}
	sorted := append([]int(nil), kids...)
	sort.Slice(sorted, func(i, j int) bool {
		if widths[sorted[i]] != widths[sorted[j]] {
			return widths[sorted[i]] < widths[sorted[j]]
		}
		return sorted[i] < sorted[j]
	})
	out := make([]int, 0, len(sorted))
	var back []int
	for i, k := range sorted {
		if i%2 == 0 {
			out = append(out, k)
		} else {
			back = append(back, k)
		}
	}
	for i := len(back) - 1; i >= 0; i-- {
		out = append(out, back[i])
	}
	return out
}

// bbox returns the tree's bounding box relative to its root.
func (t *peeledTree) bbox(lg *layoutGraph) (minX, minY, maxX, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, v := range t.nodes {
		minX = math.Min(minX, t.relX[v]-lg.w[v]/2)
		maxX = math.Max(maxX, t.relX[v]+lg.w[v]/2)
		minY = math.Min(minY, t.relY[v]-lg.h[v]/2)
		maxY = math.Max(maxY, t.relY[v]+lg.h[v]/2)
	}
	return minX, minY, maxX, maxY
}

// candidateDirs lists growth directions in preference order, starting
// with the preferred direction.
func candidateDirs(preferred CompassDir) []CompassDir {
	dirs := []CompassDir{South, East, West, North}
	out := []CompassDir{preferred}
	for _, d := range dirs {
		if d != preferred {
			out = append(out, d)
		}
	}
	return out
}

// placeTree chooses a growth direction for the tree at its root's
// current position, scoring each candidate by the overlap its bounding
// box would have with already-placed nodes. This replaces libdialect's
// face-based tree placement. The chosen layout is applied to the node
// coordinates, and the occupied set is updated.
func placeTree(st *layoutState, t *peeledTree, occupied map[int]bool, opts HolaOpts) {
	lg := st.lg
	nodeSep := opts.TreeLayoutScalarNodeSep * st.iel
	rankSep := opts.TreeLayoutScalarRankSep * st.iel
	rx, ry := lg.x[t.root], lg.y[t.root]

	bestCost := math.Inf(1)
	var bestDir CompassDir
	for i, dir := range candidateDirs(opts.PreferredTreeGrowthDir) {
		t.symmetricLayout(lg, dir, nodeSep, rankSep, opts.PreferConvexTrees)
		minX, minY, maxX, maxY := t.bbox(lg)
		cost := overlapArea(lg, occupied, t, rx+minX, ry+minY, rx+maxX, ry+maxY)
		cost += float64(i) * treeDirPreferenceCost * st.iel * st.iel
		if cost < bestCost {
			bestCost = cost
			bestDir = dir
		}
	}

	t.symmetricLayout(lg, bestDir, nodeSep, rankSep, opts.PreferConvexTrees)
	for _, v := range t.nodes {
		lg.x[v] = rx + t.relX[v]
		lg.y[v] = ry + t.relY[v]
		occupied[v] = true
	}
	// Rigid-cluster constraints: every tree node keeps a fixed offset
	// from the root, so subsequent projections move the tree as a unit.
	for _, v := range t.nodes {
		if v == t.root {
			continue
		}
		st.cons = append(st.cons,
			sepConstraint{d: dimX, left: t.root, right: v, gap: t.relX[v], equality: true},
			sepConstraint{d: dimY, left: t.root, right: v, gap: t.relY[v], equality: true})
	}
}

// treeDirPreferenceCost is the area-equivalent penalty (in units of
// IEL^2) for each step away from the preferred growth direction.
const treeDirPreferenceCost = 0.05

func overlapArea(lg *layoutGraph, occupied map[int]bool, t *peeledTree, minX, minY, maxX, maxY float64) float64 {
	inTree := make(map[int]bool, len(t.nodes))
	for _, v := range t.nodes {
		inTree[v] = true
	}
	var area float64
	for v := range occupied {
		if inTree[v] {
			continue
		}
		ox := math.Min(maxX, lg.x[v]+lg.w[v]/2) - math.Max(minX, lg.x[v]-lg.w[v]/2)
		oy := math.Min(maxY, lg.y[v]+lg.h[v]/2) - math.Max(minY, lg.y[v]-lg.h[v]/2)
		if ox > 0 && oy > 0 {
			area += ox * oy
		}
	}
	return area
}
