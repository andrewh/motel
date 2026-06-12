package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGraphCommand(t *testing.T) {
	t.Parallel()

	t.Run("produces HTML to stdout", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)

		root := rootCmd()
		root.SetArgs([]string{"graph", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		html := out.String()
		assert.True(t, strings.HasPrefix(html, "<!DOCTYPE html>"))
		assert.Contains(t, html, "p5.min.js")
		assert.Contains(t, html, `"gateway"`)
		assert.Contains(t, html, `"backend"`)
		assert.NotContains(t, html, "/*GRAPH_DATA*/")
		assert.NotContains(t, html, "GRAPH_TITLE")
	})

	t.Run("produces HTML to file", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		outFile := filepath.Join(t.TempDir(), "graph.html")

		root := rootCmd()
		root.SetArgs([]string{"graph", "-o", outFile, path})

		err := root.Execute()
		require.NoError(t, err)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(data), "<!DOCTYPE html>"))
	})

	t.Run("missing config file", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"graph", "/nonexistent.yaml"})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("no args shows error", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"graph"})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("escapes title", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		err := renderGraphHTML(&buf, graphData{}, `topo<>&.yaml`, false, nil)
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "topo&lt;&gt;&amp;.yaml")
		assert.NotContains(t, buf.String(), "topo<>&.yaml")
	})
}

func TestBuildGraphData(t *testing.T) {
	t.Parallel()

	buildTopo := func(t *testing.T, yaml string) *synth.Topology {
		t.Helper()
		path := writeTestConfig(t, yaml)
		cfg, err := synth.LoadConfig(path)
		require.NoError(t, err)
		require.NoError(t, synth.ValidateConfig(cfg))
		topo, err := synth.BuildTopology(cfg, nil)
		require.NoError(t, err)
		return topo
	}

	t.Run("nodes edges and roots", func(t *testing.T) {
		t.Parallel()
		topo := buildTopo(t, validConfig)
		data := buildGraphData(topo)

		require.Len(t, data.Nodes, 2)
		assert.Equal(t, "backend", data.Nodes[0].ID)
		assert.False(t, data.Nodes[0].IsRoot)
		assert.Equal(t, "gateway", data.Nodes[1].ID)
		assert.True(t, data.Nodes[1].IsRoot)
		assert.Equal(t, []string{"GET /users"}, data.Nodes[1].Operations)

		require.Len(t, data.Edges, 1)
		edge := data.Edges[0]
		assert.Equal(t, "gateway", edge.Source)
		assert.Equal(t, "backend", edge.Target)
		assert.Equal(t, 1.0, edge.Weight)
		assert.False(t, edge.Async)
		require.Len(t, edge.Calls, 1)
		assert.Equal(t, "GET /users", edge.Calls[0].From)
		assert.Equal(t, "list", edge.Calls[0].To)
	})

	t.Run("aggregates calls between same services", func(t *testing.T) {
		t.Parallel()
		topo := buildTopo(t, `
version: 1
services:
  api:
    operations:
      read:
        duration: 10ms
        calls:
          - db.query
      write:
        duration: 10ms
        calls:
          - db.exec
  db:
    operations:
      query:
        duration: 5ms
      exec:
        duration: 5ms
traffic:
  rate: 10/s
`)
		data := buildGraphData(topo)

		require.Len(t, data.Edges, 1)
		assert.Equal(t, 2.0, data.Edges[0].Weight)
		assert.Len(t, data.Edges[0].Calls, 2)
	})

	t.Run("layered layout positions", func(t *testing.T) {
		t.Parallel()
		topo := buildTopo(t, `
version: 1
services:
  gateway:
    operations:
      handle:
        duration: 10ms
        calls:
          - api.process
  api:
    operations:
      process:
        duration: 10ms
        calls:
          - db.query
  db:
    operations:
      query:
        duration: 5ms
traffic:
  rate: 10/s
`)
		data := buildGraphData(topo)

		byID := make(map[string]graphNode, len(data.Nodes))
		for _, n := range data.Nodes {
			byID[n.ID] = n
		}
		assert.Equal(t, 0.0, byID["gateway"].X)
		assert.Equal(t, 0.5, byID["api"].X)
		assert.Equal(t, 1.0, byID["db"].X)
		assert.Equal(t, 0.5, byID["gateway"].Y)
	})

	t.Run("shared dependency placed at longest call depth", func(t *testing.T) {
		t.Parallel()
		topo := buildTopo(t, `
version: 1
services:
  gateway:
    operations:
      handle:
        duration: 10ms
        calls:
          - api.process
          - cache.get
  api:
    operations:
      process:
        duration: 10ms
        calls:
          - cache.get
  cache:
    operations:
      get:
        duration: 1ms
traffic:
  rate: 10/s
`)
		data := buildGraphData(topo)

		byID := make(map[string]graphNode, len(data.Nodes))
		for _, n := range data.Nodes {
			byID[n.ID] = n
		}
		assert.Equal(t, 0.0, byID["gateway"].X)
		assert.Equal(t, 0.5, byID["api"].X)
		assert.Equal(t, 1.0, byID["cache"].X, "cache sits at its longest call depth")
	})

	t.Run("async and probability modifiers", func(t *testing.T) {
		t.Parallel()
		topo := buildTopo(t, `
version: 1
services:
  api:
    operations:
      submit:
        duration: 10ms
        calls:
          - target: queue.publish
            async: true
            probability: 0.5
  queue:
    operations:
      publish:
        duration: 2ms
traffic:
  rate: 10/s
`)
		data := buildGraphData(topo)

		require.Len(t, data.Edges, 1)
		edge := data.Edges[0]
		assert.True(t, edge.Async)
		assert.Equal(t, 0.5, edge.Weight)
		require.Len(t, edge.Calls, 1)
		assert.True(t, edge.Calls[0].Async)
		assert.Equal(t, 0.5, edge.Calls[0].Probability)
	})
}
