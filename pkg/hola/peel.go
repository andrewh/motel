package hola

import "sort"

// Port of libdialect's peeling.cpp: repeatedly cut leaves off the graph,
// reconnecting them in a separate forest. What remains of the input is
// the core; the forest components are the peeled trees, each rooted at
// the node that still belongs to the core (or at the final centre, when
// the whole graph was a tree).

// peeledTree is a rooted tree produced by peeling. Node values are
// indices into the full layoutGraph; the root is also a core node unless
// the entire graph was a tree.
type peeledTree struct {
	root     int
	nodes    []int
	children map[int][]int
	relX     map[int]float64
	relY     map[int]float64
	growth   CompassDir
}

func (t *peeledTree) size() int { return len(t.nodes) }

// peel removes all trees from the graph, returning them along with the
// set of remaining core node indices.
func peel(lg *layoutGraph) (trees []*peeledTree, core map[int]bool) {
	n := lg.n()
	deg := make([]int, n)
	removed := make([]bool, n)
	for i := 0; i < n; i++ {
		deg[i] = lg.degree(i)
	}

	// Forest under construction: stems recorded as (root, leaf) pairs,
	// with serial numbers assigned as in PeeledNode so that each
	// component's root is the node with the maximal serial number.
	type stem struct{ leaf, root int }
	var forest [][2]int
	serial := make(map[int]int, n)
	nextSerial := 0
	assign := func(i int) {
		if _, ok := serial[i]; !ok {
			serial[i] = nextSerial
			nextSerial++
		}
	}

	takeLeaves := func() []int {
		var leaves []int
		for i := 0; i < n; i++ {
			if !removed[i] && deg[i] == 1 {
				leaves = append(leaves, i)
			}
		}
		return leaves
	}

	leaves := takeLeaves()
	for len(leaves) > 0 {
		var stems []stem
		for _, leaf := range leaves {
			for _, v := range lg.adj[leaf] {
				if !removed[v] {
					stems = append(stems, stem{leaf: leaf, root: v})
					break
				}
			}
		}
		for _, leaf := range leaves {
			removed[leaf] = true
		}
		for _, s := range stems {
			deg[s.root]--
		}
		empty := true
		for i := 0; i < n; i++ {
			if !removed[i] {
				empty = false
				break
			}
		}
		if empty {
			// Pure tree with a double centre U--V: keep only one of the
			// two mirrored stems.
			stems = stems[:1]
		}
		for _, s := range stems {
			assign(s.leaf)
			assign(s.root)
			serial[s.root] = nextSerial
			nextSerial++
			forest = append(forest, [2]int{s.root, s.leaf})
		}
		leaves = takeLeaves()
	}

	core = make(map[int]bool)
	for i := 0; i < n; i++ {
		if !removed[i] {
			core[i] = true
		}
	}

	trees = buildTreesFromForest(forest, serial)
	return trees, core
}

// buildTreesFromForest splits the stem forest into connected components
// and roots each at its maximal-serial node.
func buildTreesFromForest(forest [][2]int, serial map[int]int) []*peeledTree {
	adj := make(map[int][]int)
	for _, e := range forest {
		adj[e[0]] = append(adj[e[0]], e[1])
		adj[e[1]] = append(adj[e[1]], e[0])
	}
	var members []int
	for v := range adj {
		members = append(members, v)
	}
	sort.Ints(members)

	seen := make(map[int]bool)
	var trees []*peeledTree
	for _, start := range members {
		if seen[start] {
			continue
		}
		var comp []int
		queue := []int{start}
		seen[start] = true
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			comp = append(comp, u)
			nbrs := append([]int(nil), adj[u]...)
			sort.Ints(nbrs)
			for _, v := range nbrs {
				if !seen[v] {
					seen[v] = true
					queue = append(queue, v)
				}
			}
		}
		sort.Ints(comp)
		root := comp[0]
		for _, v := range comp {
			if serial[v] >= serial[root] {
				root = v
			}
		}
		t := &peeledTree{root: root, nodes: comp, children: make(map[int][]int)}
		fillChildren(t, adj)
		trees = append(trees, t)
	}
	sort.Slice(trees, func(i, j int) bool {
		if trees[i].size() != trees[j].size() {
			return trees[i].size() > trees[j].size()
		}
		return trees[i].root < trees[j].root
	})
	return trees
}

func fillChildren(t *peeledTree, adj map[int][]int) {
	visited := map[int]bool{t.root: true}
	queue := []int{t.root}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		nbrs := append([]int(nil), adj[u]...)
		sort.Ints(nbrs)
		for _, v := range nbrs {
			if !visited[v] {
				visited[v] = true
				t.children[u] = append(t.children[u], v)
				queue = append(queue, v)
			}
		}
	}
}
