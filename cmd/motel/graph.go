// Experimental topology visualiser: renders the service graph as an
// interactive HTML page using p5.js (issue 134 proof of concept).
package main

import (
	"cmp"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/spf13/cobra"
)

//go:embed graph.html
var graphHTMLTemplate string

const (
	graphDataPlaceholder  = "/*GRAPH_DATA*/null"
	graphTitlePlaceholder = "GRAPH_TITLE"
)

func graphCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "graph <topology.yaml | URL>",
		Short: "Render the topology service graph as an interactive HTML page (experimental)",
		Long: "Render the topology service graph as an interactive HTML page (experimental).\n\n" +
			"Services are drawn as nodes and call relationships as directed edges in a\n" +
			"layered left-to-right layout rendered with p5.js. Edge thickness reflects\n" +
			"call volume, dashed edges are async calls, and hovering a service lists its\n" +
			"operations.\n\n" +
			"The output is a single self-contained HTML file (p5.js is loaded from a CDN).\n" +
			"The topology source can be a local file path or an HTTP/HTTPS URL.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file or URL\n\nUsage: motel graph <topology.yaml | URL>")
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGraph(cmd, args[0], output)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "output file path (default: stdout)")

	return cmd
}

func runGraph(cmd *cobra.Command, configPath, output string) error {
	cfg, err := synth.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := synth.ValidateConfig(cfg); err != nil {
		return err
	}
	topo, err := buildTopology(cfg, "")
	if err != nil {
		return err
	}

	data := buildGraphData(topo)

	var w io.Writer = cmd.OutOrStdout()
	if output != "" {
		f, err := os.Create(output) //nolint:gosec // user-supplied output path is expected
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close() //nolint:errcheck // best-effort close on write
		w = f
	}

	title := filepath.Base(configPath)
	return renderGraphHTML(w, data, title)
}

// graphNode is a service node in the visualised graph. X and Y are
// normalised layout coordinates in [0,1], computed by the layered layout.
type graphNode struct {
	ID         string   `json:"id"`
	Operations []string `json:"operations"`
	IsRoot     bool     `json:"isRoot"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
}

// graphCall is a single operation-level call along an edge.
type graphCall struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	Probability float64 `json:"probability"`
	Count       int     `json:"count"`
	Async       bool    `json:"async"`
}

// graphEdge aggregates all calls between a pair of services.
type graphEdge struct {
	Source string      `json:"source"`
	Target string      `json:"target"`
	Weight float64     `json:"weight"`
	Async  bool        `json:"async"`
	Calls  []graphCall `json:"calls"`
}

// graphData is the JSON payload embedded in the HTML page.
type graphData struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

// buildGraphData flattens a resolved topology into nodes and service-level
// edges. Edges aggregate operation-level calls between the same pair of
// services; weight is the expected number of calls per invocation. An edge is
// marked async only when every call it aggregates is async.
func buildGraphData(topo *synth.Topology) graphData {
	rootServices := make(map[string]bool, len(topo.Roots))
	for _, op := range topo.Roots {
		rootServices[op.Service.Name] = true
	}

	var data graphData
	edges := make(map[string]*graphEdge)

	for _, svcName := range slices.Sorted(maps.Keys(topo.Services)) {
		svc := topo.Services[svcName]
		node := graphNode{
			ID:         svcName,
			Operations: slices.Sorted(maps.Keys(svc.Operations)),
			IsRoot:     rootServices[svcName],
		}
		data.Nodes = append(data.Nodes, node)

		for _, opName := range node.Operations {
			op := svc.Operations[opName]
			for _, call := range op.Calls {
				target := call.Operation.Service.Name
				key := svcName + "\x00" + target
				edge, ok := edges[key]
				if !ok {
					edge = &graphEdge{Source: svcName, Target: target, Async: true}
					edges[key] = edge
				}
				count := call.Count
				if count < 1 {
					count = 1
				}
				// Probability 0 means unconditional in the resolved topology
				probability := call.Probability
				if probability <= 0 {
					probability = 1
				}
				edge.Weight += probability * float64(count)
				edge.Async = edge.Async && call.Async
				edge.Calls = append(edge.Calls, graphCall{
					From:        opName,
					To:          call.Operation.Name,
					Probability: probability,
					Count:       count,
					Async:       call.Async,
				})
			}
		}
	}

	for _, key := range slices.Sorted(maps.Keys(edges)) {
		data.Edges = append(data.Edges, *edges[key])
	}

	layoutGraph(&data, serviceLayers(topo))
	return data
}

// serviceLayers assigns each service a column index: the call depth of its
// shallowest operation, measured as the longest path from any root. The
// operation graph is acyclic (BuildTopology rejects cycles), so the longest
// path is well defined even when the service-level graph contains cycles.
func serviceLayers(topo *synth.Topology) map[string]int {
	opDepth := make(map[*synth.Operation]int)
	var visit func(op *synth.Operation, depth int)
	visit = func(op *synth.Operation, depth int) {
		if d, seen := opDepth[op]; seen && d >= depth {
			return
		}
		opDepth[op] = depth
		for _, call := range op.Calls {
			visit(call.Operation, depth+1)
		}
	}
	for _, root := range topo.Roots {
		visit(root, 0)
	}

	layers := make(map[string]int, len(topo.Services))
	for op, depth := range opDepth {
		name := op.Service.Name
		if cur, seen := layers[name]; !seen || depth < cur {
			layers[name] = depth
		}
	}
	for name := range topo.Services {
		if _, seen := layers[name]; !seen {
			layers[name] = 0
		}
	}
	return layers
}

const barycenterSweeps = 4

// layoutGraph computes normalised coordinates for a layered left-to-right
// layout: services in columns by call depth, ordered within each column by
// repeated barycenter sweeps to reduce edge crossings.
func layoutGraph(data *graphData, layers map[string]int) {
	maxLayer := 0
	for _, l := range layers {
		maxLayer = max(maxLayer, l)
	}

	columns := make([][]int, maxLayer+1)
	for i, n := range data.Nodes {
		l := layers[n.ID]
		columns[l] = append(columns[l], i)
	}

	neighbours := make(map[int][]int)
	index := make(map[string]int, len(data.Nodes))
	for i, n := range data.Nodes {
		index[n.ID] = i
	}
	for _, e := range data.Edges {
		s, t := index[e.Source], index[e.Target]
		if s == t {
			continue
		}
		neighbours[s] = append(neighbours[s], t)
		neighbours[t] = append(neighbours[t], s)
	}

	// rank holds each node's vertical position normalised to [0,1)
	rank := make([]float64, len(data.Nodes))
	setRanks := func(col []int) {
		for pos, i := range col {
			rank[i] = (float64(pos) + 0.5) / float64(len(col))
		}
	}
	for _, col := range columns {
		setRanks(col)
	}

	barycenter := func(i int) float64 {
		ns := neighbours[i]
		if len(ns) == 0 {
			return rank[i]
		}
		sum := 0.0
		for _, n := range ns {
			sum += rank[n]
		}
		return sum / float64(len(ns))
	}

	for sweep := 0; sweep < barycenterSweeps; sweep++ {
		for _, col := range columns {
			slices.SortStableFunc(col, func(a, b int) int {
				if c := cmp.Compare(barycenter(a), barycenter(b)); c != 0 {
					return c
				}
				return cmp.Compare(data.Nodes[a].ID, data.Nodes[b].ID)
			})
			setRanks(col)
		}
	}

	for l, col := range columns {
		x := 0.5
		if maxLayer > 0 {
			x = float64(l) / float64(maxLayer)
		}
		for pos, i := range col {
			data.Nodes[i].X = x
			data.Nodes[i].Y = (float64(pos) + 0.5) / float64(len(col))
		}
	}
}

func renderGraphHTML(w io.Writer, data graphData, title string) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encoding graph data: %w", err)
	}

	page := strings.Replace(graphHTMLTemplate, graphDataPlaceholder, string(payload), 1)
	page = strings.ReplaceAll(page, graphTitlePlaceholder, htmlEscape(title))

	_, err = io.WriteString(w, page)
	return err
}

func htmlEscape(s string) string {
	return xmlEscape(s)
}
