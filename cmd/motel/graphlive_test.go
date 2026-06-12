package main

import (
	"bufio"
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func liveTestTopology(t *testing.T) *synth.Topology {
	t.Helper()
	path := writeTestConfig(t, validConfig)
	cfg, err := synth.LoadConfig(path)
	require.NoError(t, err)
	require.NoError(t, synth.ValidateConfig(cfg))
	topo, err := synth.BuildTopology(cfg, nil)
	require.NoError(t, err)
	return topo
}

func TestLiveObserver(t *testing.T) {
	t.Parallel()

	topo := liveTestTopology(t)
	obs := newLiveObserver(topo)

	snap := obs.snapshot()
	require.Contains(t, snap.Services, "gateway")
	require.Contains(t, snap.Services, "backend")
	assert.Equal(t, int64(0), snap.Services["gateway"].Spans)

	obs.Observe(synth.SpanInfo{Service: "gateway", Operation: "GET /users", Duration: 30 * time.Millisecond})
	obs.Observe(synth.SpanInfo{Service: "gateway", Operation: "GET /users", Duration: 10 * time.Millisecond, IsError: true})
	obs.Observe(synth.SpanInfo{Service: "backend", Operation: "list", Duration: 20 * time.Millisecond, Kind: trace.SpanKindServer})

	snap = obs.snapshot()
	assert.Equal(t, int64(2), snap.Services["gateway"].Spans)
	assert.Equal(t, int64(1), snap.Services["gateway"].Errors)
	assert.Equal(t, 40.0, snap.Services["gateway"].DurationMs)
	assert.Equal(t, int64(1), snap.Services["backend"].Spans)
	assert.Positive(t, snap.TimestampMs)
}

func TestGraphServerMux(t *testing.T) {
	t.Parallel()

	topo := liveTestTopology(t)
	capture := &graphCapture{obs: newLiveObserver(topo)}
	capture.sample()
	mux, err := graphServerMux(topo, "test.yaml", capture)
	require.NoError(t, err)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("serves live graph page", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/")
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // test cleanup

		buf := new(strings.Builder)
		_, err = bufio.NewReader(resp.Body).WriteTo(buf)
		require.NoError(t, err)
		html := buf.String()
		assert.Contains(t, html, "const live = true;")
		assert.Contains(t, html, `"gateway"`)
		assert.Contains(t, html, "test.yaml")
	})

	t.Run("unknown path is 404", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/nope")
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		assert.Equal(t, 404, resp.StatusCode)
	})

	t.Run("events endpoint streams snapshots", func(t *testing.T) {
		capture.obs.Observe(synth.SpanInfo{Service: "gateway", Operation: "GET /users", Duration: time.Millisecond})
		capture.sample()

		resp, err := srv.Client().Get(srv.URL + "/events")
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck // test cleanup

		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(line, "data: {"))
		assert.Contains(t, line, `"gateway"`)
		assert.Contains(t, line, `"spans":`)
	})
}

func TestRenderGraphHTMLLiveFlag(t *testing.T) {
	t.Parallel()

	var static, live strings.Builder
	require.NoError(t, renderGraphHTML(&static, graphData{}, "t.yaml", false, nil))
	require.NoError(t, renderGraphHTML(&live, graphData{}, "t.yaml", true, nil))

	assert.Contains(t, static.String(), "const live = false;")
	assert.Contains(t, live.String(), "const live = true;")
	assert.Contains(t, static.String(), "const timeline = /*TIMELINE_DATA*/null;")
}

func TestRenderGraphHTMLTimeline(t *testing.T) {
	t.Parallel()

	timeline := []liveSnapshot{
		{TimestampMs: 1000, Services: map[string]liveServiceStats{"gateway": {Spans: 1}}},
		{TimestampMs: 1500, Services: map[string]liveServiceStats{"gateway": {Spans: 5, Errors: 1}}},
	}
	var out strings.Builder
	require.NoError(t, renderGraphHTML(&out, graphData{}, "t.yaml", false, timeline))

	html := out.String()
	assert.Contains(t, html, `const timeline = [{"timestampMs":1000`)
	assert.Contains(t, html, `"spans":5`)
}

// testRunLog is a minimal valid run log: header, three spans, stats trailer.
const testRunLog = `{"type":"run","v":1,"motel":"test","topology":"t.yaml","startMs":1000}
{"type":"span","t":0,"d":30,"svc":"gateway","op":"GET /users"}
{"type":"span","t":5,"d":20,"svc":"backend","op":"list","psvc":"gateway","pop":"GET /users"}
{"type":"span","t":600,"d":10,"svc":"gateway","op":"GET /users","err":true}
{"type":"plan","t":610,"kind":"retry","svc":"backend","op":"list"}
{"type":"stats","t":1200,"stats":{"traces":2,"spans":3}}
`

func TestGraphReplayCommand(t *testing.T) {
	t.Parallel()

	topoPath := writeTestConfig(t, validConfig)
	recPath := filepath.Join(t.TempDir(), "rec.jsonl")
	require.NoError(t, os.WriteFile(recPath, []byte(testRunLog), 0o600))

	root := rootCmd()
	root.SetArgs([]string{"graph", "--replay", recPath, topoPath})
	var out bytes.Buffer
	root.SetOut(&out)

	require.NoError(t, root.Execute())
	html := out.String()
	assert.Contains(t, html, `const timeline = [{"timestampMs":1500`)
	assert.Contains(t, html, "const live = false;")
}
