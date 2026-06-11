package main

import (
	"bufio"
	"net/http/httptest"
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
	obs := newLiveObserver(topo)
	mux, err := graphServerMux(topo, "test.yaml", obs)
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
		obs.Observe(synth.SpanInfo{Service: "gateway", Operation: "GET /users", Duration: time.Millisecond})

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
	require.NoError(t, renderGraphHTML(&static, graphData{}, "t.yaml", false))
	require.NoError(t, renderGraphHTML(&live, graphData{}, "t.yaml", true))

	assert.Contains(t, static.String(), "const live = false;")
	assert.Contains(t, live.String(), "const live = true;")
}
