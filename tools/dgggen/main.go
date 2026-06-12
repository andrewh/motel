// dgggen generates synthetic call-graph JSON in DGG format
// (https://github.com/dufanrong/DGG) with shape distributions drawn from
// production measurements reported in Du et al., "DGG: A Novel Framework
// for Microservice Call Graph Generation Based on Realistic Distributions"
// (ICWS 2024), which characterises Alibaba production traces.
//
// The generated corpus is stratified rather than frequency-matched: deep
// (15+ levels) and wide (hundreds of nodes) graphs are oversampled relative
// to their production frequency so that a corpus of ~100 graphs still
// exercises the tail of the distribution. Production characteristics
// represented:
//
//   - Call graph depth: most graphs ≤ 6 levels, tail reaching ~20
//   - Graph size: heavy-tailed, from single-service graphs to 400+ nodes
//   - Fan-out: concentrated at 1-3 children, hubs reaching ~50
//   - Repeated calls: mostly 1, heavy tail reaching into the hundreds
//   - Services with multiple interfaces (~49% have >2 in production)
//
// Output is consumable by dgg2motel:
//
//	go run ./tools/dgggen -n 100 -out /tmp/dgg-prod
//	go run ./tools/dgg2motel -dir /tmp/dgg-prod -out /tmp/dgg-prod-topologies
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
)

// DGG JSON structures, matching the format dgg2motel consumes.

type dggGraph struct {
	Nodes []dggNode `json:"nodes"`
	Edges []dggEdge `json:"edges"`
	Num   int       `json:"num"`
}

type dggNode struct {
	Node  string `json:"node"`
	Label string `json:"label"`
}

type dggEdge struct {
	RPCID   string `json:"rpcid"`
	UM      string `json:"um"`
	DM      string `json:"dm"`
	Time    int    `json:"time"`
	Compara string `json:"compara"`
}

// Service type labels used by DGG.
const (
	labelNormal    = "normal"
	labelMemcached = "Memcached"
	labelBlackhole = "blackhole"
	labelRelay     = "relay"
)

// maxStaticSpans bounds the static worst-case spans per trace of a generated
// graph, keeping it under motel's DefaultMaxSpansPerTrace (10,000) with room
// to spare so converted topologies run without span bounding.
const maxStaticSpans = 5000

// maxFanOut caps children per node, matching the upper bound reported for
// Meta in the DGG paper.
const maxFanOut = 50

// maxEffectiveFanOut caps the sum of call multiplicities across one node's
// children — what motel counts as fan-out. The bound tracks the P99
// repeated-call counts (374-469) reported in the DGG paper.
const maxEffectiveFanOut = 500

// bucket is a weighted integer range for sampling heavy-tailed distributions.
type bucket struct {
	weight   int
	min, max int
}

// depthBuckets samples graph depth in node levels (root = 1). Production
// depth is ≤ 6 for >99.99% of graphs with a max around 15; the tail here is
// oversampled so a small corpus covers it.
var depthBuckets = []bucket{
	{8, 1, 1},
	{20, 2, 3},
	{24, 4, 5},
	{18, 6, 7},
	{12, 8, 10},
	{10, 11, 14},
	{6, 15, 17},
	{2, 18, 20},
}

// sizeBuckets samples target node count. Heavy-tailed: most graphs are
// small, the tail reaches hundreds of nodes.
var sizeBuckets = []bucket{
	{35, 3, 10},
	{30, 11, 40},
	{22, 41, 150},
	{10, 151, 300},
	{3, 301, 450},
}

func sampleBucket(r *rand.Rand, buckets []bucket) int {
	total := 0
	for _, b := range buckets {
		total += b.weight
	}
	pick := r.Intn(total)
	for _, b := range buckets {
		if pick < b.weight {
			return b.min + r.Intn(b.max-b.min+1)
		}
		pick -= b.weight
	}
	last := buckets[len(buckets)-1]
	return last.max
}

// genNode is the in-memory tree node used during construction.
type genNode struct {
	name     string
	label    string
	depth    int
	children []*genNode
	mult     int // call multiplicity on the edge from the parent
}

func main() {
	nFlag := flag.Int("n", 100, "number of graphs to generate")
	seedFlag := flag.Int64("seed", 1, "random seed (deterministic output)")
	outFlag := flag.String("out", "", "output directory (required)")
	flag.Parse()

	if *outFlag == "" {
		fmt.Fprintln(os.Stderr, "usage: dgggen -n 100 -seed 1 -out path/")
		os.Exit(1)
	}
	if err := os.MkdirAll(*outFlag, 0o755); err != nil {
		fatal(err)
	}

	r := rand.New(rand.NewSource(*seedFlag))
	for i := range *nFlag {
		g := generate(r)
		data, err := json.MarshalIndent(g, "", "    ")
		if err != nil {
			fatal(err)
		}
		path := filepath.Join(*outFlag, fmt.Sprintf("graph_%03d.json", i+1))
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			fatal(err)
		}
	}
	fmt.Fprintf(os.Stderr, "generated %d graphs in %s\n", *nFlag, *outFlag)
}

// generate builds one call graph as a tree rooted at a single entry service.
func generate(r *rand.Rand) dggGraph {
	depth := sampleBucket(r, depthBuckets)
	size := sampleBucket(r, sizeBuckets)
	if size < depth {
		size = depth
	}
	if depth == 1 {
		size = 1
	}

	// Build a spine guaranteeing the sampled depth, then attach the
	// remaining nodes to random eligible parents. A small preferential-
	// attachment bias produces the heavy-tailed fan-out seen in production.
	root := &genNode{depth: 1, mult: 1}
	nodes := []*genNode{root}
	prev := root
	for d := 2; d <= depth; d++ {
		n := &genNode{depth: d, mult: 1}
		prev.children = append(prev.children, n)
		nodes = append(nodes, n)
		prev = n
	}
	for len(nodes) < size {
		parent := pickParent(r, nodes, depth)
		if parent == nil {
			break
		}
		n := &genNode{depth: parent.depth + 1, mult: 1}
		parent.children = append(parent.children, n)
		nodes = append(nodes, n)
	}

	assignLabels(r, root, nodes)
	assignMultiplicities(r, nodes)
	clampSpans(root)
	assignNames(r, root)

	g := dggGraph{Num: 1}
	g.Nodes = append(g.Nodes, dggNode{Node: "USER", Label: labelRelay})
	for _, n := range nodes {
		g.Nodes = append(g.Nodes, dggNode{Node: n.name, Label: n.label})
	}
	g.Edges = append(g.Edges, dggEdge{
		RPCID: "0", UM: "USER", DM: root.name, Time: 1, Compara: "http",
	})
	emitEdges(r, &g, root, "0")
	return g
}

// pickParent selects an attachment point for a new node. Parents must be
// shallower than the sampled depth so the graph never exceeds it, and below
// the fan-out cap. 30% of the time the most-connected of three candidates
// is chosen, biasing growth toward hubs.
func pickParent(r *rand.Rand, nodes []*genNode, depth int) *genNode {
	eligible := make([]*genNode, 0, len(nodes))
	for _, n := range nodes {
		if n.depth < depth && len(n.children) < maxFanOut {
			eligible = append(eligible, n)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	if r.Intn(100) < 30 {
		best := eligible[r.Intn(len(eligible))]
		for range 2 {
			c := eligible[r.Intn(len(eligible))]
			if len(c.children) > len(best.children) {
				best = c
			}
		}
		return best
	}
	return eligible[r.Intn(len(eligible))]
}

// assignLabels gives each node a DGG service type. Leaves are often caches
// or blackholes; interior nodes are almost always normal services.
func assignLabels(r *rand.Rand, root *genNode, nodes []*genNode) {
	for _, n := range nodes {
		switch {
		case n == root:
			if r.Intn(100) < 20 {
				n.label = labelRelay
			} else {
				n.label = labelNormal
			}
		case len(n.children) == 0:
			switch p := r.Intn(100); {
			case p < 35:
				n.label = labelMemcached
			case p < 50:
				n.label = labelBlackhole
			default:
				n.label = labelNormal
			}
		default:
			if r.Intn(100) < 5 {
				n.label = labelRelay
			} else {
				n.label = labelNormal
			}
		}
	}
}

// assignMultiplicities sets the call count on each node's inbound edge.
// The paper reports repeated calls with a heavy tail (P99 in the hundreds).
// Large multiplicities only appear on leaf edges, where they add spans
// rather than multiplying entire subtrees.
func assignMultiplicities(r *rand.Rand, nodes []*genNode) {
	for _, n := range nodes {
		if n.depth == 1 {
			continue
		}
		if len(n.children) == 0 {
			switch p := r.Intn(100); {
			case p < 50:
				n.mult = 1
			case p < 85:
				n.mult = 2 + r.Intn(4) // 2-5
			case p < 97:
				n.mult = 6 + r.Intn(45) // 6-50
			default:
				n.mult = 100 + r.Intn(301) // 100-400
			}
		} else if r.Intn(100) < 5 {
			n.mult = 2
		}
	}

	// Clamp each node's effective fan-out (sum of child multiplicities)
	// by halving the largest child multiplicity until it fits.
	for _, n := range nodes {
		for {
			total := 0
			var largest *genNode
			for _, c := range n.children {
				total += c.mult
				if largest == nil || c.mult > largest.mult {
					largest = c
				}
			}
			if total <= maxEffectiveFanOut || largest == nil || largest.mult <= 1 {
				break
			}
			largest.mult /= 2
		}
	}
}

// staticSpans computes the worst-case spans per trace: every node
// contributes the product of multiplicities along its path from the root.
func staticSpans(n *genNode, factor int) int {
	factor *= n.mult
	total := factor
	for _, c := range n.children {
		total += staticSpans(c, factor)
	}
	return total
}

// clampSpans halves the largest leaf multiplicity until the static span
// count fits under maxStaticSpans.
func clampSpans(root *genNode) {
	for staticSpans(root, 1) > maxStaticSpans {
		var largest *genNode
		var walk func(*genNode)
		walk = func(n *genNode) {
			if len(n.children) == 0 && (largest == nil || n.mult > largest.mult) {
				largest = n
			}
			for _, c := range n.children {
				walk(c)
			}
		}
		walk(root)
		if largest == nil || largest.mult <= 1 {
			return
		}
		largest.mult /= 2
		if largest.mult < 1 {
			largest.mult = 1
		}
	}
}

// assignNames produces DGG-style node names. Normal services are sometimes
// reused with a _funcN suffix so converted topologies contain services with
// multiple operations, matching the ~49% of production services that expose
// more than two interfaces.
func assignNames(r *rand.Rand, root *genNode) {
	type svc struct {
		base  string
		funcs int
	}
	var normals []*svc
	counters := map[string]int{}

	newName := func(n *genNode) string {
		switch n.label {
		case labelMemcached:
			counters["mc"]++
			return fmt.Sprintf("MS_Memcached.%d", counters["mc"])
		case labelBlackhole:
			counters["bh"]++
			return fmt.Sprintf("MS_blackhole.%d", counters["bh"])
		case labelRelay:
			counters["relay"]++
			return fmt.Sprintf("MS_relay+1.%d", counters["relay"])
		default:
			// Reuse an existing normal service as a new function 35% of
			// the time, except for the root, which keeps its own service.
			if n != root && len(normals) > 0 && r.Intn(100) < 35 {
				s := normals[r.Intn(len(normals))]
				s.funcs++
				return fmt.Sprintf("%s_func%d", s.base, s.funcs)
			}
			counters["normal"]++
			base := fmt.Sprintf("MS_normal+1.%d", counters["normal"])
			normals = append(normals, &svc{base: base})
			return base
		}
	}

	var walk func(*genNode)
	walk = func(n *genNode) {
		n.name = newName(n)
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(root)
}

// emitEdges writes edges depth-first with hierarchical rpcids ("0.3.1" is
// the first child of the third child of the root call).
func emitEdges(r *rand.Rand, g *dggGraph, n *genNode, rpcid string) {
	for i, c := range n.children {
		childID := fmt.Sprintf("%s.%d", rpcid, i+1)
		g.Edges = append(g.Edges, dggEdge{
			RPCID:   childID,
			UM:      n.name,
			DM:      c.name,
			Time:    c.mult,
			Compara: protocolFor(r, c.label),
		})
		emitEdges(r, g, c, childID)
	}
}

func protocolFor(r *rand.Rand, label string) string {
	switch label {
	case labelMemcached:
		return "mc"
	case labelBlackhole:
		return "rpc"
	default:
		if r.Intn(100) < 40 {
			return "http"
		}
		return "rpc"
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "dgggen: %v\n", err)
	os.Exit(1)
}
