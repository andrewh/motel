// Experimental topology visualiser: renders the service graph as an
// interactive HTML page using p5.js (issue 134 proof of concept).
package main

import (
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
			"force-directed layout powered by p5.js. Edge thickness reflects call volume,\n" +
			"dashed edges are async calls, and hovering a service lists its operations.\n\n" +
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

// graphNode is a service node in the visualised graph.
type graphNode struct {
	ID         string   `json:"id"`
	Operations []string `json:"operations"`
	IsRoot     bool     `json:"isRoot"`
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
	return data
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
