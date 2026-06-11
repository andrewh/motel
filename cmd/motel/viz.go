package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/spf13/cobra"
)

//go:embed viz.html
var vizTemplate string

const vizDataPlaceholder = "/*__GRAPH_DATA__*/null"

// vizNode is a service in the rendered graph.
type vizNode struct {
	ID         string   `json:"id"`
	Operations []string `json:"operations"`
	Root       bool     `json:"root"`
}

// vizCall is a single operation-to-operation call within an edge.
type vizCall struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	Probability float64 `json:"probability"`
	Async       bool    `json:"async"`
}

// vizEdge aggregates all calls from one service to another.
type vizEdge struct {
	Source string    `json:"source"`
	Target string    `json:"target"`
	Weight float64   `json:"weight"`
	Calls  []vizCall `json:"calls"`
}

// vizGraph is the JSON payload embedded in the generated HTML page.
type vizGraph struct {
	Title string    `json:"title"`
	Nodes []vizNode `json:"nodes"`
	Edges []vizEdge `json:"edges"`
}

func vizCmd() *cobra.Command {
	var (
		output string
		serve  string
	)

	cmd := &cobra.Command{
		Use:   "viz <topology.yaml | URL>",
		Short: "Render the service graph as an interactive HTML page",
		Long: "Render the topology service graph as a self-contained HTML page using p5.js.\n\n" +
			"Services are nodes, call relationships are directed edges. Edge thickness\n" +
			"reflects call probability. Drag nodes to rearrange; hover for details.\n\n" +
			"The topology source can be a local file path or an HTTP/HTTPS URL.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file or URL\n\nUsage: motel viz <topology.yaml | URL>")
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runViz(cmd, args[0], output, serve)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "output file path (default: stdout)")
	cmd.Flags().StringVar(&serve, "serve", "", "serve the page over HTTP on this address (e.g. :8080) instead of writing a file")

	return cmd
}

func runViz(cmd *cobra.Command, configPath, output, serve string) error {
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

	page, err := renderVizHTML(topo, filepath.Base(configPath))
	if err != nil {
		return err
	}

	if serve != "" {
		return serveViz(cmd, serve, page)
	}

	var w io.Writer = cmd.OutOrStdout()
	if output != "" {
		f, err := os.Create(output) //nolint:gosec // user-supplied output path is expected
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close() //nolint:errcheck // best-effort close on write
		w = f
	}
	_, err = io.WriteString(w, page)
	return err
}

func renderVizHTML(topo *synth.Topology, title string) (string, error) {
	graph := buildVizGraph(topo, title)
	data, err := json.Marshal(graph)
	if err != nil {
		return "", fmt.Errorf("encoding graph: %w", err)
	}
	if !strings.Contains(vizTemplate, vizDataPlaceholder) {
		return "", fmt.Errorf("viz template is missing the graph data placeholder")
	}
	// Escape "</" so the JSON payload cannot terminate the enclosing script tag.
	safe := strings.ReplaceAll(string(data), "</", `<\/`)
	return strings.Replace(vizTemplate, vizDataPlaceholder, safe, 1), nil
}

func buildVizGraph(topo *synth.Topology, title string) vizGraph {
	roots := make(map[string]bool, len(topo.Roots))
	for _, op := range topo.Roots {
		roots[op.Service.Name] = true
	}

	graph := vizGraph{Title: title}
	type edgeKey struct{ source, target string }
	edges := make(map[edgeKey]*vizEdge)

	for _, name := range slices.Sorted(maps.Keys(topo.Services)) {
		svc := topo.Services[name]
		node := vizNode{ID: name, Root: roots[name]}
		for _, opName := range slices.Sorted(maps.Keys(svc.Operations)) {
			op := svc.Operations[opName]
			node.Operations = append(node.Operations, opName)
			for _, call := range op.Calls {
				key := edgeKey{source: name, target: call.Operation.Service.Name}
				e, ok := edges[key]
				if !ok {
					e = &vizEdge{Source: key.source, Target: key.target}
					edges[key] = e
				}
				// The engine treats a zero probability as an unconditional call.
				probability := call.Probability
				if probability <= 0 {
					probability = 1
				}
				e.Weight += probability
				e.Calls = append(e.Calls, vizCall{
					From:        opName,
					To:          call.Operation.Name,
					Probability: probability,
					Async:       call.Async,
				})
			}
		}
		graph.Nodes = append(graph.Nodes, node)
	}

	for _, src := range graph.Nodes {
		for _, dst := range graph.Nodes {
			if e, ok := edges[edgeKey{source: src.ID, target: dst.ID}]; ok {
				graph.Edges = append(graph.Edges, *e)
			}
		}
	}
	return graph
}

const vizReadHeaderTimeout = 5 * time.Second

func serveViz(cmd *cobra.Command, addr, page string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: vizReadHeaderTimeout}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Serving topology visualisation at http://%s/ (Ctrl-C to stop)\n", listener.Addr())

	go func() {
		<-cmd.Context().Done()
		_ = server.Close()
	}()

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
