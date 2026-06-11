package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const vizTestConfig = `
version: 1
services:
  gateway:
    operations:
      request:
        duration: 10ms
        calls:
          - backend.handle
          - target: cache.get
            probability: 0.5
            async: true
  backend:
    operations:
      handle:
        duration: 5ms
  cache:
    operations:
      get:
        duration: 1ms
traffic:
  rate: 10/s
`

func TestVizCommand(t *testing.T) {
	t.Parallel()

	t.Run("produces HTML to stdout", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, vizTestConfig)

		root := rootCmd()
		root.SetArgs([]string{"viz", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		html := out.String()
		assert.True(t, strings.HasPrefix(html, "<!DOCTYPE html>"))
		assert.Contains(t, html, "p5")
		assert.Contains(t, html, `"gateway"`)
		assert.NotContains(t, html, "__GRAPH_DATA__")
	})

	t.Run("produces HTML to file", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, vizTestConfig)
		outFile := filepath.Join(t.TempDir(), "viz.html")

		root := rootCmd()
		root.SetArgs([]string{"viz", "-o", outFile, path})

		err := root.Execute()
		require.NoError(t, err)
		assert.FileExists(t, outFile)
	})

	t.Run("missing config file", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"viz", "/nonexistent.yaml"})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("no args shows error", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"viz"})

		err := root.Execute()
		require.Error(t, err)
	})
}

func TestBuildVizGraph(t *testing.T) {
	t.Parallel()

	buildTestTopology := func(t *testing.T) *synth.Topology {
		t.Helper()
		cfg, err := synth.LoadConfig(writeTestConfig(t, vizTestConfig))
		require.NoError(t, err)
		require.NoError(t, synth.ValidateConfig(cfg))
		topo, err := synth.BuildTopology(cfg, nil)
		require.NoError(t, err)
		return topo
	}

	t.Run("nodes are sorted and roots marked", func(t *testing.T) {
		t.Parallel()
		graph := buildVizGraph(buildTestTopology(t), "test.yaml")

		require.Len(t, graph.Nodes, 3)
		assert.Equal(t, "backend", graph.Nodes[0].ID)
		assert.Equal(t, "cache", graph.Nodes[1].ID)
		assert.Equal(t, "gateway", graph.Nodes[2].ID)
		assert.False(t, graph.Nodes[0].Root)
		assert.True(t, graph.Nodes[2].Root)
		assert.Equal(t, []string{"request"}, graph.Nodes[2].Operations)
	})

	t.Run("edges carry probability and async", func(t *testing.T) {
		t.Parallel()
		graph := buildVizGraph(buildTestTopology(t), "test.yaml")

		require.Len(t, graph.Edges, 2)
		byTarget := map[string]vizEdge{}
		for _, e := range graph.Edges {
			assert.Equal(t, "gateway", e.Source)
			byTarget[e.Target] = e
		}

		backend := byTarget["backend"]
		require.Len(t, backend.Calls, 1)
		assert.Equal(t, 1.0, backend.Calls[0].Probability, "unconditional call should normalise to probability 1")
		assert.False(t, backend.Calls[0].Async)
		assert.Equal(t, 1.0, backend.Weight)

		cache := byTarget["cache"]
		require.Len(t, cache.Calls, 1)
		assert.Equal(t, 0.5, cache.Calls[0].Probability)
		assert.True(t, cache.Calls[0].Async)
	})

	t.Run("graph JSON round-trips", func(t *testing.T) {
		t.Parallel()
		graph := buildVizGraph(buildTestTopology(t), "test.yaml")

		data, err := json.Marshal(graph)
		require.NoError(t, err)
		var decoded vizGraph
		require.NoError(t, json.Unmarshal(data, &decoded))
		assert.Equal(t, graph, decoded)
	})
}

func TestRenderVizHTML(t *testing.T) {
	t.Parallel()

	t.Run("escapes closing script sequences in data", func(t *testing.T) {
		t.Parallel()
		cfg := `
version: 1
services:
  svc:
    operations:
      "</script><script>alert(1)":
        duration: 1ms
traffic:
  rate: 1/s
`
		c, err := synth.LoadConfig(writeTestConfig(t, cfg))
		require.NoError(t, err)
		require.NoError(t, synth.ValidateConfig(c))
		topo, err := synth.BuildTopology(c, nil)
		require.NoError(t, err)

		html, err := renderVizHTML(topo, "test.yaml")
		require.NoError(t, err)
		assert.NotContains(t, html, "</script><script>alert(1)")
	})
}
