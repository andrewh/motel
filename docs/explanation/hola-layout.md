# HOLA layout engine

`pkg/hola` is a Go port of the HOLA (Human-like Orthogonal Layout
Algorithm) engine from the [adaptagrams](https://github.com/mjwybrow/adaptagrams)
`libdialect` library (Kieffer, Dwyer, Marriott, Wybrow: *HOLA: Human-like
Orthogonal Network Layout*, IEEE TVCG 2016). It computes orthogonal
layouts of node-link graphs — node positions plus orthogonal edge
routes — suitable for rendering service topology diagrams.

## Usage

```go
g := hola.NewGraph()
g.AddNode("gateway", 120, 40)
g.AddNode("orders", 120, 40)
g.AddNode("db", 120, 40)
g.AddEdge("gateway", "orders")
g.AddEdge("orders", "db")
if err := hola.DoHOLA(g); err != nil { ... }
for _, n := range g.Nodes() { ... } // n.X, n.Y are box centres
for _, e := range g.Edges() { ... } // e.Route is an orthogonal polyline
```

`DoHOLAWithOpts` accepts a `HolaOpts`, mirroring the options of
libdialect's `opts.h` with the same defaults (tree growth direction,
node padding scalar, preferred aspect ratio, near-alignment, and so on).

## Pipeline

The port follows the stage structure of `doHOLA` in libdialect's
`hola.cpp`:

1. **Peel** trees off the graph, leaving a biconnected-ish core
   (`peel.go`, a direct port of `peeling.cpp`). A graph that is entirely
   one tree short-circuits to a symmetric tree layout.
2. **Destress** the core: stress majorisation alternated with VPSC
   projection onto the constraint set, first freely, then with node
   overlap prevention (`stress.go`). The projection solver (`vpsc.go`)
   is a port of libvpsc's block merge/split algorithm, including
   equality (alignment) constraints and Lagrange-multiplier splitting.
3. **Orthogonalise**: hubs (degree >= 3) have their incident edges
   assigned to compass directions and constrained accordingly, then the
   remaining links are aligned greedily, cheapest first, refusing
   alignments that would create coincident edges (`aca.go`, after
   `OrthoHubLayout` and `ACALayout::createAlignments`).
4. **Reattach trees**: each peeled tree gets a symmetric layout
   (convex subtree ordering, ranks separated by the ideal edge length)
   and is placed at its root in the growth direction with the least
   overlap, held rigid by fixed-offset equality constraints while a
   neighbour-stress destress compacts the drawing (`tree.go`).
5. **Near-align** almost-orthogonal edges, rotate the drawing for the
   preferred aspect ratio, and translate the upper-left corner to the
   origin (`hola.go`).
6. **Route** edges orthogonally with A* over an orthogonal visibility
   grid, with per-bend penalties and ports spread along node sides
   (`route.go`).

All stages iterate over nodes in sorted-ID order, so layouts are
deterministic for a given input graph.

## Differences from the C++ original

Two stages are simplified relative to libdialect:

- The planarisation and face-analysis machinery (`planarise.cpp`,
  `faces.cpp`, `treeplacement.cpp`) is replaced by direct tree
  reattachment with rigid clusters and VPSC-driven expansion. Tree
  placement scores growth directions by overlap area rather than by
  face geometry.
- libavoid's incremental orthogonal router is replaced by per-edge A*
  search over a visibility grid built from node box boundaries. There
  is no crossing penalty or shared-path nudging beyond port spreading.

These trade some aesthetic refinement for a small, dependency-free
implementation; the constraint engine, peeling, symmetric tree layout,
and the overall pipeline match the original's structure.
