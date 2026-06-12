package hola

import "math"

// AspectRatioClass expresses a preferred overall aspect ratio for the
// finished layout.
type AspectRatioClass int

// Aspect ratio preferences.
const (
	AspectRatioNone AspectRatioClass = iota
	AspectRatioPortrait
	AspectRatioLandscape
)

// HolaOpts mirrors the options struct of libdialect's opts.h, restricted
// to the options this port supports. Zero values are not meaningful;
// start from DefaultHolaOpts.
type HolaOpts struct {
	DefaultTreeGrowthDir        CompassDir
	TreeLayoutScalarNodeSep     float64
	TreeLayoutScalarRankSep     float64
	PreferConvexTrees           bool
	UseACAForLinks              bool
	RoutingScalarSegmentPenalty float64
	RoutingAbsNudgingDistance   float64
	DoNearAlign                 bool
	AlignReps                   int
	NearAlignScalarKinkWidth    float64
	NodePaddingScalar           float64
	PreferredAspectRatio        AspectRatioClass
	PreferredTreeGrowthDir      CompassDir
	PutUlcAtOrigin              bool
}

// DefaultHolaOpts returns the defaults from libdialect's opts.h.
func DefaultHolaOpts() HolaOpts {
	return HolaOpts{
		DefaultTreeGrowthDir:        South,
		TreeLayoutScalarNodeSep:     0.25,
		TreeLayoutScalarRankSep:     1.0,
		PreferConvexTrees:           true,
		UseACAForLinks:              true,
		RoutingScalarSegmentPenalty: 0.5,
		RoutingAbsNudgingDistance:   4.0,
		DoNearAlign:                 true,
		AlignReps:                   2,
		NearAlignScalarKinkWidth:    0.25,
		NodePaddingScalar:           0.25,
		PreferredAspectRatio:        AspectRatioLandscape,
		PreferredTreeGrowthDir:      South,
		PutUlcAtOrigin:              true,
	}
}

const preRoutingGapIELScalar = 0.125

// DoHOLA computes a HOLA layout for the graph with default options,
// setting node positions and orthogonal edge routes.
func DoHOLA(g *Graph) error {
	return DoHOLAWithOpts(g, DefaultHolaOpts())
}

// DoHOLAWithOpts computes a HOLA layout for the graph, the port of
// dialect::doHOLA. The pipeline: peel trees from the core; lay the core
// out by constrained stress minimisation; orthogonalise hubs and links
// with alignment constraints; reattach symmetrically laid out trees;
// near-align; rotate for the preferred aspect ratio; route edges
// orthogonally.
func DoHOLAWithOpts(g *Graph, opts HolaOpts) error {
	if g.NumEdges() == 0 {
		return nil
	}
	iel := g.iel()
	lg := newLayoutGraph(g)
	nodePadding := opts.NodePaddingScalar * iel
	lg.pad(nodePadding, nodePadding)

	trees, core := peel(lg)

	if len(trees) == 1 && trees[0].size() == lg.n() {
		layoutPureTree(g, lg, trees[0], iel, nodePadding, opts)
		return nil
	}

	coreLG, toFull := subLayoutGraph(lg, core)
	seedPositions(coreLG, iel)
	cst := newLayoutState(coreLG, iel)

	// Free destress, then destress with overlap prevention.
	cst.destress(colaOpts{})
	cst.destress(colaOpts{preventOverlaps: true})

	// Orthogonal hub layout, then dissipate the accumulated stress.
	da := make(dirAssignment)
	aligned := make(map[[2]int]dim)
	orthoHubLayout(cst, da, aligned)
	cst.destress(colaOpts{preventOverlaps: true, solidAlignedEdges: true})

	// Link configuration.
	if opts.UseACAForLinks {
		acaCreateAlignments(cst, da, aligned)
	}

	// Destress with a pre-routing gap so channels stay open.
	preRoutingGap := preRoutingGapIELScalar * iel
	coreLG.pad(preRoutingGap, preRoutingGap)
	cst.destress(colaOpts{preventOverlaps: true, solidAlignedEdges: true})
	coreLG.pad(-preRoutingGap, -preRoutingGap)

	// Transfer the core layout and its constraints to the full graph.
	for ci, fi := range toFull {
		lg.x[fi] = coreLG.x[ci]
		lg.y[fi] = coreLG.y[ci]
	}
	fst := newLayoutState(lg, iel)
	for _, c := range cst.cons {
		c.left, c.right = toFull[c.left], toFull[c.right]
		fst.cons = append(fst.cons, c)
	}
	daFull := make(dirAssignment)
	for ci, dirs := range da {
		for d := range dirs {
			daFull.take(toFull[ci], d)
		}
	}
	alignedFull := make(map[[2]int]dim)
	for k, d := range aligned {
		alignedFull[edgeKey(toFull[k[0]], toFull[k[1]])] = d
	}

	// Reattach the trees and compact with neighbour stress.
	occupied := make(map[int]bool, len(core))
	for v := range core {
		occupied[v] = true
	}
	for _, t := range trees {
		placeTree(fst, t, occupied, opts)
	}
	compact := colaOpts{preventOverlaps: true, solidAlignedEdges: true, useNeighbourStress: true}
	fst.destress(compact)

	if opts.DoNearAlign {
		kink := opts.NearAlignScalarKinkWidth * iel
		for i := 0; i < opts.AlignReps; i++ {
			nearAlignments(fst, alignedFull, daFull, kink)
			fst.destress(compact)
		}
	}
	fst.removeOverlaps(compact)

	if quarterTurns := rotateForAspectRatio(lg, trees, opts); quarterTurns%2 == 1 {
		// Nodes are not rotated with the layout: restore each box's own
		// orientation and repair any overlaps this introduces.
		for i := 0; i < lg.n(); i++ {
			lg.w[i], lg.h[i] = lg.h[i], lg.w[i]
		}
		newLayoutState(lg, iel).removeOverlaps(colaOpts{})
	}

	lg.pad(-nodePadding, -nodePadding)
	writeBack(g, lg)
	routeAndFinish(g, lg, iel, opts)
	return nil
}

// layoutPureTree handles the case where the whole graph is one tree.
func layoutPureTree(g *Graph, lg *layoutGraph, t *peeledTree, iel, nodePadding float64, opts HolaOpts) {
	t.symmetricLayout(lg, opts.DefaultTreeGrowthDir,
		opts.TreeLayoutScalarNodeSep*iel, opts.TreeLayoutScalarRankSep*iel,
		opts.PreferConvexTrees)
	for _, v := range t.nodes {
		lg.x[v] = t.relX[v]
		lg.y[v] = t.relY[v]
	}
	lg.pad(-nodePadding, -nodePadding)
	writeBack(g, lg)
	routeAndFinish(g, lg, iel, opts)
}

// seedPositions arranges nodes on a circle when the current positions
// are degenerate, giving stress minimisation a deterministic start.
func seedPositions(lg *layoutGraph, iel float64) {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for i := 0; i < lg.n(); i++ {
		minX = math.Min(minX, lg.x[i])
		maxX = math.Max(maxX, lg.x[i])
		minY = math.Min(minY, lg.y[i])
		maxY = math.Max(maxY, lg.y[i])
	}
	if maxX-minX > iel/2 || maxY-minY > iel/2 {
		return
	}
	n := float64(lg.n())
	radius := n * iel / (2 * math.Pi)
	for i := 0; i < lg.n(); i++ {
		theta := 2 * math.Pi * float64(i) / n
		lg.x[i] = radius * math.Cos(theta)
		lg.y[i] = radius * math.Sin(theta)
	}
}

// subLayoutGraph extracts the induced subgraph on the kept nodes,
// returning it along with the mapping from sub indices to full indices.
func subLayoutGraph(lg *layoutGraph, keep map[int]bool) (*layoutGraph, []int) {
	var toFull []int
	for i := 0; i < lg.n(); i++ {
		if keep[i] {
			toFull = append(toFull, i)
		}
	}
	toSub := make(map[int]int, len(toFull))
	sub := &layoutGraph{index: make(map[string]int, len(toFull))}
	for si, fi := range toFull {
		toSub[fi] = si
		sub.ids = append(sub.ids, lg.ids[fi])
		sub.index[lg.ids[fi]] = si
		sub.x = append(sub.x, lg.x[fi])
		sub.y = append(sub.y, lg.y[fi])
		sub.w = append(sub.w, lg.w[fi])
		sub.h = append(sub.h, lg.h[fi])
	}
	sub.adj = make([][]int, len(toFull))
	for _, e := range lg.edges {
		u, ok1 := toSub[e[0]]
		v, ok2 := toSub[e[1]]
		if ok1 && ok2 {
			sub.edges = append(sub.edges, [2]int{u, v})
			sub.adj[u] = append(sub.adj[u], v)
			sub.adj[v] = append(sub.adj[v], u)
		}
	}
	return sub, toFull
}

// rotateForAspectRatio rotates the layout a quarter or half turn when
// that better matches the preferred aspect ratio and tree growth
// direction, following the logic of doHOLA.
func rotateForAspectRatio(lg *layoutGraph, trees []*peeledTree, opts HolaOpts) int {
	if opts.PreferredAspectRatio == AspectRatioNone {
		return 0
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for i := 0; i < lg.n(); i++ {
		minX = math.Min(minX, lg.x[i]-lg.w[i]/2)
		maxX = math.Max(maxX, lg.x[i]+lg.w[i]/2)
		minY = math.Min(minY, lg.y[i]-lg.h[i]/2)
		maxY = math.Max(maxY, lg.y[i]+lg.h[i]/2)
	}
	w, h := maxX-minX, maxY-minY

	counts := make(map[CompassDir]int)
	for _, t := range trees {
		counts[t.growth] += t.size()
	}
	q := opts.PreferredTreeGrowthDir
	quarterTurnsCW := 0
	if (w < h && opts.PreferredAspectRatio == AspectRatioLandscape) ||
		(h < w && opts.PreferredAspectRatio == AspectRatioPortrait) {
		// Trees growing 90 degrees away from the preferred direction
		// decide whether to turn clockwise or anticlockwise.
		if counts[q.rotateCW(3)] >= counts[q.rotateCW(1)] {
			quarterTurnsCW = 1
		} else {
			quarterTurnsCW = 3
		}
	} else if counts[q.rotateCW(2)] > counts[q] {
		quarterTurnsCW = 2
	}
	for t := 0; t < quarterTurnsCW; t++ {
		rotate90cw(lg)
	}
	for _, t := range trees {
		t.growth = t.growth.rotateCW(quarterTurnsCW)
	}
	return quarterTurnsCW
}

func rotate90cw(lg *layoutGraph) {
	for i := 0; i < lg.n(); i++ {
		lg.x[i], lg.y[i] = -lg.y[i], lg.x[i]
		lg.w[i], lg.h[i] = lg.h[i], lg.w[i]
	}
}

func writeBack(g *Graph, lg *layoutGraph) {
	for i, id := range lg.ids {
		n := g.nodes[id]
		n.X, n.Y = lg.x[i], lg.y[i]
	}
}

// routeAndFinish routes all edges orthogonally and translates the
// drawing so its upper-left corner sits at the origin.
func routeAndFinish(g *Graph, lg *layoutGraph, iel float64, opts HolaOpts) {
	router := newOrthoRouter(lg, opts.RoutingAbsNudgingDistance, opts.RoutingScalarSegmentPenalty*iel)
	router.routeAll(g)
	if !opts.PutUlcAtOrigin {
		return
	}
	x, y, _, _ := g.BoundingBox()
	for _, id := range g.order {
		g.nodes[id].X -= x
		g.nodes[id].Y -= y
	}
	for _, e := range g.edges {
		for i := range e.Route {
			e.Route[i].X -= x
			e.Route[i].Y -= y
		}
	}
}
