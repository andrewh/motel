// Live topology graph server: serves the graph page during a run and
// streams per-service span statistics over Server-Sent Events.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/andrewh/motel/pkg/synth"
)

const (
	liveUpdateInterval     = 500 * time.Millisecond
	graphReadHeaderTimeout = 5 * time.Second
)

// liveServiceStats holds cumulative per-service counters. The browser
// computes rates from deltas between successive snapshots.
type liveServiceStats struct {
	Spans      int64   `json:"spans"`
	Errors     int64   `json:"errors"`
	DurationMs float64 `json:"durationMs"`
}

// liveSnapshot is one SSE payload.
type liveSnapshot struct {
	TimestampMs int64                       `json:"timestampMs"`
	Services    map[string]liveServiceStats `json:"services"`
}

// liveObserver aggregates span statistics per service. Observe may be
// called from multiple goroutines in realtime mode.
type liveObserver struct {
	mu       sync.Mutex
	services map[string]*liveServiceStats
}

func newLiveObserver(topo *synth.Topology) *liveObserver {
	services := make(map[string]*liveServiceStats, len(topo.Services))
	for name := range topo.Services {
		services[name] = &liveServiceStats{}
	}
	return &liveObserver{services: services}
}

func (o *liveObserver) Observe(info synth.SpanInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.services[info.Service]
	if !ok {
		s = &liveServiceStats{}
		o.services[info.Service] = s
	}
	s.Spans++
	if info.IsError {
		s.Errors++
	}
	s.DurationMs += float64(info.Duration) / float64(time.Millisecond)
}

func (o *liveObserver) snapshot() liveSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	services := make(map[string]liveServiceStats, len(o.services))
	for name, s := range o.services {
		services[name] = *s
	}
	return liveSnapshot{
		TimestampMs: time.Now().UnixMilli(),
		Services:    services,
	}
}

// serveEvents streams snapshots as Server-Sent Events until the client
// disconnects.
func (o *liveObserver) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ticker := time.NewTicker(liveUpdateInterval)
	defer ticker.Stop()

	for {
		payload, err := json.Marshal(o.snapshot())
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return
		}
		flusher.Flush()

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// graphServerMux builds the HTTP handler serving the live graph page and
// the SSE stats stream.
func graphServerMux(topo *synth.Topology, title string, obs *liveObserver) (*http.ServeMux, error) {
	var page bytes.Buffer
	if err := renderGraphHTML(&page, buildGraphData(topo), title, true); err != nil {
		return nil, err
	}
	html := page.Bytes()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(html)
	})
	mux.HandleFunc("/events", obs.serveEvents)
	return mux, nil
}

// startLiveGraph starts the live graph HTTP server and returns the span
// observer feeding it plus a shutdown function.
func startLiveGraph(addr string, topo *synth.Topology, configPath string) (*liveObserver, func(), error) {
	obs := newLiveObserver(topo)
	mux, err := graphServerMux(topo, filepath.Base(configPath), obs)
	if err != nil {
		return nil, nil, err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("starting live graph server: %w", err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: graphReadHeaderTimeout}
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "live graph server error: %v\n", serveErr)
		}
	}()
	fmt.Fprintf(os.Stderr, "live graph available at http://%s/\n", ln.Addr())

	shutdown := func() { _ = srv.Close() }
	return obs, shutdown, nil
}
