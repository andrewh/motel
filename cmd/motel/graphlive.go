// Live topology graph server and run log recording. During a run a
// graphSession captures per-service span statistics for the live page
// (streamed over Server-Sent Events) and optionally writes a lossless
// run log via runRecorder for later replay.
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

// graphSession ties together the optional live server and optional run log
// recorder for one run. Register its observers with the engine, then call
// close with the final stats when the run completes.
type graphSession struct {
	observers   []synth.SpanObserver
	recorder    *runRecorder
	srv         *http.Server
	stopSampler func()
	closeOnce   sync.Once
	closeErr    error
}

// startGraphSession starts the live graph server (if addr is non-empty)
// and the run log recorder (if recordPath is non-empty).
func startGraphSession(addr, recordPath string, topo *synth.Topology, configPath string, scenarios []synth.Scenario, opts runOptions) (*graphSession, error) {
	s := &graphSession{}

	if recordPath != "" {
		// One clock read for the whole header: StartMs is the epoch all
		// record offsets are relative to (simulated run start).
		epoch := time.Now().Add(opts.timeOffset)
		header := runHeader{
			Type:         recordTypeRun,
			Version:      runLogVersion,
			Motel:        version,
			Topology:     filepath.Base(configPath),
			Seed:         opts.seed,
			StartMs:      epoch.UnixMilli(),
			TimeOffsetMs: opts.timeOffset.Milliseconds(),
			Realtime:     opts.realtime,
			Scenarios:    scenarioWindows(scenarios),
		}
		recorder, err := newRunRecorder(recordPath, header, epoch)
		if err != nil {
			return nil, err
		}
		s.recorder = recorder
		s.observers = append(s.observers, recorder)
	}

	if addr != "" {
		obs := newLiveObserver(topo)
		capture := &graphCapture{obs: obs}
		s.observers = append(s.observers, obs)

		mux, err := graphServerMux(topo, filepath.Base(configPath), capture)
		if err != nil {
			s.closeRecorder()
			return nil, err
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			s.closeRecorder()
			return nil, fmt.Errorf("starting live graph server: %w", err)
		}

		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(liveUpdateInterval)
			defer ticker.Stop()
			capture.sample()
			for {
				select {
				case <-stop:
					capture.sample()
					return
				case <-ticker.C:
					capture.sample()
				}
			}
		}()
		s.stopSampler = func() {
			close(stop)
			wg.Wait()
		}

		s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: graphReadHeaderTimeout}
		go func() {
			if serveErr := s.srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "live graph server error: %v\n", serveErr)
			}
		}()
		fmt.Fprintf(os.Stderr, "live graph available at http://%s/\n", ln.Addr())
	}

	return s, nil
}

func (s *graphSession) closeRecorder() {
	if s.recorder != nil {
		_ = s.recorder.finish(nil)
	}
}

// close shuts down the session, writing the stats trailer to the run log
// when stats is non-nil. Idempotent: only the first call takes effect.
func (s *graphSession) close(stats *synth.Stats) error {
	s.closeOnce.Do(func() {
		if s.stopSampler != nil {
			s.stopSampler()
		}
		if s.recorder != nil {
			s.closeErr = s.recorder.finish(stats)
		}
		if s.srv != nil {
			_ = s.srv.Close()
		}
	})
	return s.closeErr
}
