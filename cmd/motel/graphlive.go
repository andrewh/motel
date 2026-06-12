// Live topology graph server: captures per-service span statistics during
// a run, serves the graph page, streams snapshots over Server-Sent Events,
// and optionally records them to a JSON Lines file for later replay.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// liveServiceStats holds cumulative per-service counters. Viewers compute
// rates from deltas between successive snapshots.
type liveServiceStats struct {
	Spans      int64   `json:"spans"`
	Errors     int64   `json:"errors"`
	DurationMs float64 `json:"durationMs"`
}

// liveSnapshot is one timestamped sample of all service counters.
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

// graphCapture samples the observer on a timer into an append-only history.
// SSE connections stream the history from the start, so late-joining
// viewers see the whole run.
type graphCapture struct {
	obs     *liveObserver
	mu      sync.Mutex
	history []liveSnapshot
}

func (c *graphCapture) sample() liveSnapshot {
	snap := c.obs.snapshot()
	c.mu.Lock()
	c.history = append(c.history, snap)
	c.mu.Unlock()
	return snap
}

// since returns a copy of history entries from index i onward.
func (c *graphCapture) since(i int) []liveSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.history) {
		return nil
	}
	out := make([]liveSnapshot, len(c.history)-i)
	copy(out, c.history[i:])
	return out
}

// serveEvents streams the snapshot history as Server-Sent Events from the
// beginning, then follows new samples until the client disconnects.
func (c *graphCapture) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ticker := time.NewTicker(liveUpdateInterval)
	defer ticker.Stop()

	next := 0
	for {
		for _, snap := range c.since(next) {
			payload, err := json.Marshal(snap)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			next++
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
func graphServerMux(topo *synth.Topology, title string, capture *graphCapture) (*http.ServeMux, error) {
	var page bytes.Buffer
	if err := renderGraphHTML(&page, buildGraphData(topo), title, true, nil); err != nil {
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
	mux.HandleFunc("/events", capture.serveEvents)
	return mux, nil
}

// startLiveGraph starts capturing snapshots and, if addr is non-empty,
// serves the live graph page. If recordPath is non-empty, snapshots are
// appended to it as JSON Lines for later replay with motel graph --replay.
// Returns the span observer feeding the capture and a shutdown function.
func startLiveGraph(addr, recordPath string, topo *synth.Topology, configPath string) (*liveObserver, func(), error) {
	obs := newLiveObserver(topo)
	capture := &graphCapture{obs: obs}

	var record *os.File
	if recordPath != "" {
		f, err := os.Create(recordPath) //nolint:gosec // user-supplied output path is expected
		if err != nil {
			return nil, nil, fmt.Errorf("creating graph recording file: %w", err)
		}
		record = f
	}

	writeSnap := func(snap liveSnapshot) {
		if record == nil {
			return
		}
		payload, err := json.Marshal(snap)
		if err != nil {
			return
		}
		payload = append(payload, '\n')
		if _, err := record.Write(payload); err != nil {
			fmt.Fprintf(os.Stderr, "error writing graph recording: %v\n", err)
			record = nil
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(liveUpdateInterval)
		defer ticker.Stop()
		writeSnap(capture.sample())
		for {
			select {
			case <-stop:
				writeSnap(capture.sample())
				return
			case <-ticker.C:
				writeSnap(capture.sample())
			}
		}
	}()

	var srv *http.Server
	if addr != "" {
		mux, err := graphServerMux(topo, filepath.Base(configPath), capture)
		if err != nil {
			close(stop)
			wg.Wait()
			return nil, nil, err
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			close(stop)
			wg.Wait()
			return nil, nil, fmt.Errorf("starting live graph server: %w", err)
		}
		srv = &http.Server{Handler: mux, ReadHeaderTimeout: graphReadHeaderTimeout}
		go func() {
			if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "live graph server error: %v\n", serveErr)
			}
		}()
		fmt.Fprintf(os.Stderr, "live graph available at http://%s/\n", ln.Addr())
	}

	shutdown := func() {
		close(stop)
		wg.Wait()
		if record != nil {
			if err := record.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "error closing graph recording: %v\n", err)
			}
		}
		if srv != nil {
			_ = srv.Close()
		}
	}
	return obs, shutdown, nil
}

// loadTimeline reads a JSON Lines recording produced by --graph-record.
func loadTimeline(path string) ([]liveSnapshot, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied file path is expected
	if err != nil {
		return nil, fmt.Errorf("opening recording: %w", err)
	}
	defer f.Close() //nolint:errcheck // best-effort close on read-only file

	dec := json.NewDecoder(f)
	var snaps []liveSnapshot
	for {
		var s liveSnapshot
		if err := dec.Decode(&s); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("parsing recording %s: %w", path, err)
		}
		snaps = append(snaps, s)
	}
	if len(snaps) == 0 {
		return nil, fmt.Errorf("recording %s contains no snapshots", path)
	}
	return snaps, nil
}
