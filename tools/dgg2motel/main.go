// dgg2motel converts call-graph JSON files produced by DGG
// (https://github.com/dufanrong/DGG) into motel topology YAML.
//
// Usage:
//
//	go run ./tools/dgg2motel -dir sample_data/DGG_gen_cgs/20250109_150211 -out topologies/
//	go run ./tools/dgg2motel -file graph1.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DGG JSON structures.

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

// motel topology structures — just enough to marshal YAML by hand
// (avoids pulling in a YAML library for a standalone tool).

// funcRE matches DGG node names with a _funcN suffix, e.g. "MS_normal+2.1_func2".
// The greedy (.+) assumes _funcN only appears as a terminal suffix in DGG output.
var funcRE = regexp.MustCompile(`^(.+)_(func\d+)$`)

func main() {
	fileFlag := flag.String("file", "", "single DGG JSON file to convert")
	dirFlag := flag.String("dir", "", "directory tree of DGG JSON files to convert")
	outFlag := flag.String("out", "", "output directory (default: stdout for -file, required for -dir)")
	flag.Parse()

	if *fileFlag == "" && *dirFlag == "" {
		fmt.Fprintln(os.Stderr, "usage: dgg2motel -file graph.json | -dir path/to/DGG_gen_cgs/")
		os.Exit(1)
	}

	if *fileFlag != "" {
		data, err := os.ReadFile(*fileFlag)
		if err != nil {
			fatal(err)
		}
		yaml, err := convertOne(data)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", *fileFlag, err))
		}
		if *outFlag != "" {
			base := strings.TrimSuffix(filepath.Base(*fileFlag), ".json") + ".yaml"
			if err := writeFile(filepath.Join(*outFlag, base), yaml); err != nil {
				fatal(err)
			}
			fmt.Fprintf(os.Stderr, "wrote %s\n", filepath.Join(*outFlag, base))
		} else {
			fmt.Print(yaml)
		}
		return
	}

	if *outFlag == "" {
		fatal(fmt.Errorf("-out is required when using -dir"))
	}

	var converted, skipped int
	err := filepath.WalkDir(*dirFlag, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		yaml, err := convertOne(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", path, err)
			skipped++
			return nil
		}

		rel, _ := filepath.Rel(*dirFlag, path)
		outPath := filepath.Join(*outFlag, strings.TrimSuffix(rel, ".json")+".yaml")
		if err := writeFile(outPath, yaml); err != nil {
			return err
		}
		converted++
		return nil
	})
	if err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "converted %d graphs, skipped %d\n", converted, skipped)
}

func convertOne(data []byte) (string, error) {
	var g dggGraph
	if err := json.Unmarshal(data, &g); err != nil {
		return "", fmt.Errorf("parse JSON: %w", err)
	}
	if len(g.Nodes) == 0 {
		return "", fmt.Errorf("empty graph")
	}

	type childEdge struct {
		child string
		edge  dggEdge
	}

	// Group DGG nodes into motel services.
	// Node names like "MS_normal+2.1_func2" split into base service "MS_normal+2.1"
	// with operation "func2". The base node itself gets operation "handle".
	type opInfo struct {
		name  string
		label string
		calls []childEdge
	}
	type svcInfo struct {
		name string
		ops  []*opInfo
	}

	services := make(map[string]*svcInfo)
	cleanedNames := make(map[string]string) // cleaned name → raw DGG name (for collision detection)
	var svcOrder []string

	ensureService := func(baseName string) (*svcInfo, error) {
		if s, ok := services[baseName]; ok {
			return s, nil
		}
		clean := cleanName(baseName)
		if prev, exists := cleanedNames[clean]; exists {
			return nil, fmt.Errorf("service name collision: %q and %q both map to %q", prev, baseName, clean)
		}
		cleanedNames[clean] = baseName
		s := &svcInfo{name: clean}
		services[baseName] = s
		svcOrder = append(svcOrder, baseName)
		return s, nil
	}

	// Map each DGG node to a service + operation.
	nodeToOp := make(map[string]*opInfo)
	nodeToSvc := make(map[string]string) // DGG node name → base service key

	for _, n := range g.Nodes {
		if n.Node == "USER" {
			continue
		}

		var baseName, opName string
		if m := funcRE.FindStringSubmatch(n.Node); m != nil {
			baseName = m[1]
			opName = m[2]
		} else {
			baseName = n.Node
			opName = "handle"
		}

		svc, err := ensureService(baseName)
		if err != nil {
			return "", err
		}
		op := &opInfo{name: opName, label: n.Label}
		svc.ops = append(svc.ops, op)
		nodeToOp[n.Node] = op
		nodeToSvc[n.Node] = baseName
	}

	// Wire up calls.
	for _, e := range g.Edges {
		if e.UM == "USER" {
			continue
		}
		if op, ok := nodeToOp[e.UM]; ok {
			op.calls = append(op.calls, childEdge{e.DM, e})
		}
	}

	// Sort operations within each service for deterministic output.
	for _, svc := range services {
		sort.Slice(svc.ops, func(i, j int) bool {
			return svc.ops[i].name < svc.ops[j].name
		})
	}

	// Render YAML.
	var b strings.Builder
	b.WriteString("version: 1\n\nservices:\n")

	for _, svcKey := range svcOrder {
		svc := services[svcKey]
		fmt.Fprintf(&b, "  %s:\n", svc.name)
		b.WriteString("    operations:\n")

		for _, op := range svc.ops {
			fmt.Fprintf(&b, "      %s:\n", op.name)
			fmt.Fprintf(&b, "        duration: %s\n", durationForLabel(op.label))

			// Add calls.
			if len(op.calls) > 0 {
				b.WriteString("        calls:\n")
				for _, c := range op.calls {
					targetOp := nodeToOp[c.child]
					targetSvc := services[nodeToSvc[c.child]]
					if targetOp == nil || targetSvc == nil {
						continue
					}
					ref := targetSvc.name + "." + targetOp.name

					if c.edge.Time > 1 {
						fmt.Fprintf(&b, "          - target: %s\n", ref)
						fmt.Fprintf(&b, "            count: %d\n", c.edge.Time)
					} else {
						fmt.Fprintf(&b, "          - %s\n", ref)
					}
				}
			}
		}
	}

	b.WriteString("\ntraffic:\n  rate: 10/s\n")

	return b.String(), nil
}

// cleanName makes a DGG node name safe for use as a motel service name.
// "MS_normal+2.1" → "normal-2-1"
func cleanName(name string) string {
	name = strings.TrimPrefix(name, "MS_")

	// Lowercase and replace special characters with dashes.
	// e.g., "normal+2.1" → "normal-2-1", "Memcached.1" → "memcached-1"
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "+", "-")
	name = strings.ReplaceAll(name, ".", "-")
	name = strings.ReplaceAll(name, "_", "-")

	// Collapse runs of dashes.
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")

	if name == "" {
		name = "unknown"
	}
	return name
}

// durationForLabel returns a reasonable synthetic duration based on the DGG service label.
func durationForLabel(label string) string {
	switch strings.ToLower(label) {
	case "memcached":
		return "1ms +/- 500us"
	case "blackhole":
		return "5ms +/- 2ms"
	case "relay":
		return "10ms +/- 5ms"
	default: // "normal" and anything else
		return "20ms +/- 10ms"
	}
}

func writeFile(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "dgg2motel: %v\n", err)
	os.Exit(1)
}
