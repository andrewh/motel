package main

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

const testCorpusSize = 100

func genCorpus(seed int64) []dggGraph {
	r := rand.New(rand.NewSource(seed))
	graphs := make([]dggGraph, testCorpusSize)
	for i := range graphs {
		graphs[i] = generate(r)
	}
	return graphs
}

func TestDeterministic(t *testing.T) {
	a, err := json.Marshal(genCorpus(1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(genCorpus(1))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("same seed produced different corpora")
	}
}

func TestGraphValidity(t *testing.T) {
	for i, g := range genCorpus(1) {
		names := make(map[string]bool, len(g.Nodes))
		for _, n := range g.Nodes {
			if names[n.Node] {
				t.Errorf("graph %d: duplicate node name %q", i, n.Node)
			}
			names[n.Node] = true
		}

		rpcids := make(map[string]bool, len(g.Edges))
		for _, e := range g.Edges {
			if !names[e.UM] || !names[e.DM] {
				t.Errorf("graph %d: edge %s references unknown node (%s -> %s)", i, e.RPCID, e.UM, e.DM)
			}
			if rpcids[e.RPCID] {
				t.Errorf("graph %d: duplicate rpcid %q", i, e.RPCID)
			}
			rpcids[e.RPCID] = true
			if e.Time < 1 {
				t.Errorf("graph %d: edge %s has multiplicity %d", i, e.RPCID, e.Time)
			}
		}

		if len(g.Edges) != len(g.Nodes)-1 {
			t.Errorf("graph %d: %d edges for %d nodes, want a tree", i, len(g.Edges), len(g.Nodes))
		}
	}
}

func TestSpanAndFanOutBounds(t *testing.T) {
	for i, g := range genCorpus(1) {
		children := make(map[string][]dggEdge)
		for _, e := range g.Edges {
			children[e.UM] = append(children[e.UM], e)
		}

		for um, edges := range children {
			if um == "USER" {
				continue
			}
			total := 0
			for _, e := range edges {
				total += e.Time
			}
			if total > maxEffectiveFanOut {
				t.Errorf("graph %d: node %s has effective fan-out %d > %d", i, um, total, maxEffectiveFanOut)
			}
		}

		var spans func(node string, factor int) int
		spans = func(node string, factor int) int {
			total := 0
			for _, e := range children[node] {
				f := factor * e.Time
				total += f + spans(e.DM, f)
			}
			return total
		}
		if s := spans("USER", 1); s > maxStaticSpans {
			t.Errorf("graph %d: static spans %d > %d", i, s, maxStaticSpans)
		}
	}
}

// TestCorpusCoversProductionTail verifies the default-seed corpus reaches
// the deep and wide tail of the production distribution: depths of 15+
// and graphs with hundreds of spans, per the Du et al. measurements.
func TestCorpusCoversProductionTail(t *testing.T) {
	var maxDepth, maxNodes, maxSpans int
	for _, g := range genCorpus(1) {
		for _, e := range g.Edges {
			// Depth in motel terms: edges below the root operation,
			// which is the number of dots in the rpcid.
			if d := strings.Count(e.RPCID, "."); d > maxDepth {
				maxDepth = d
			}
		}
		if len(g.Nodes) > maxNodes {
			maxNodes = len(g.Nodes)
		}

		children := make(map[string][]dggEdge)
		for _, e := range g.Edges {
			children[e.UM] = append(children[e.UM], e)
		}
		var spans func(node string, factor int) int
		spans = func(node string, factor int) int {
			total := 0
			for _, e := range children[node] {
				f := factor * e.Time
				total += f + spans(e.DM, f)
			}
			return total
		}
		if s := spans("USER", 1); s > maxSpans {
			maxSpans = s
		}
	}

	if maxDepth < 15 {
		t.Errorf("corpus max depth %d, want >= 15", maxDepth)
	}
	if maxNodes < 150 {
		t.Errorf("corpus max node count %d, want >= 150", maxNodes)
	}
	if maxSpans < 500 {
		t.Errorf("corpus max spans %d, want >= 500", maxSpans)
	}
}
