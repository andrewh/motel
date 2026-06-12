// Run log recording: a lossless JSON Lines event log of a simulation run.
// The header captures run metadata (seed, scenario windows); span and plan
// records capture per-event ground truth; a stats trailer closes the log.
// Aggregation into timeline buckets happens at render time, not at record
// time, so a recording can be re-rendered at any resolution.
package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andrewh/motel/pkg/synth"
)

// runLogVersion is the run log format version.
const runLogVersion = 1

// Record type tags in the run log.
const (
	recordTypeRun   = "run"
	recordTypeSpan  = "span"
	recordTypePlan  = "plan"
	recordTypeStats = "stats"
)

// runHeader is the first record of a run log. StartMs is the simulated
// run start (wall-clock start plus the configured time offset), the epoch
// for all span and plan record offsets; TimeOffsetMs preserves the offset
// so wall-clock times remain recoverable.
type runHeader struct {
	Type         string           `json:"type"`
	Version      int              `json:"v"`
	Motel        string           `json:"motel"`
	Topology     string           `json:"topology"`
	Seed         uint64           `json:"seed,omitempty"`
	StartMs      int64            `json:"startMs"`
	TimeOffsetMs int64            `json:"timeOffsetMs,omitempty"`
	Realtime     bool             `json:"realtime,omitempty"`
	Scenarios    []scenarioWindow `json:"scenarios,omitempty"`
}

// scenarioWindow records when a scenario is active, as offsets from run start.
type scenarioWindow struct {
	Name    string `json:"name"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

// spanRecord is one emitted span. T is the span start as a millisecond
// offset from run start in simulated time; D is the duration in
// milliseconds. TraceID and SpanID cross-reference the emitted telemetry.
type spanRecord struct {
	Type            string  `json:"type"`
	T               float64 `json:"t"`
	D               float64 `json:"d"`
	Service         string  `json:"svc"`
	Operation       string  `json:"op"`
	ParentService   string  `json:"psvc,omitempty"`
	ParentOperation string  `json:"pop,omitempty"`
	Error           bool    `json:"err,omitempty"`
	TraceID         string  `json:"tr,omitempty"`
	SpanID          string  `json:"sp,omitempty"`
}

// planRecord is one plan-phase decision (timeout, retry, queue rejection,
// circuit breaker trip) at a millisecond offset from run start.
type planRecord struct {
	Type      string  `json:"type"`
	T         float64 `json:"t"`
	Kind      string  `json:"kind"`
	Service   string  `json:"svc"`
	Operation string  `json:"op"`
}

// statsRecord is the trailing record holding the engine's final counters.
type statsRecord struct {
	Type  string       `json:"type"`
	T     float64      `json:"t"`
	Stats *synth.Stats `json:"stats"`
}

// runLog is a fully parsed recording.
type runLog struct {
	Header runHeader
	Spans  []spanRecord
	Plans  []planRecord
	Stats  *statsRecord
}

// gzipSuffix enables transparent compression of run logs by file name.
const gzipSuffix = ".gz"

// runRecorder writes a run log as events arrive. It implements
// synth.SpanObserver and synth.PlanEventObserver; Observe may be called
// from multiple goroutines in realtime mode.
type runRecorder struct {
	mu    sync.Mutex
	file  *os.File
	gz    *gzip.Writer
	enc   *json.Encoder
	epoch time.Time
	err   error
}

// newRunRecorder creates the run log file, writes the header, and returns
// a recorder ready to observe events. epoch is the run start in simulated
// time; all record offsets are relative to it.
func newRunRecorder(path string, header runHeader, epoch time.Time) (*runRecorder, error) {
	f, err := os.Create(path) //nolint:gosec // user-supplied output path is expected
	if err != nil {
		return nil, fmt.Errorf("creating run log: %w", err)
	}

	r := &runRecorder{file: f, epoch: epoch}
	var w io.Writer = f
	if strings.HasSuffix(path, gzipSuffix) {
		r.gz = gzip.NewWriter(f)
		w = r.gz
	}
	r.enc = json.NewEncoder(w)

	if err := r.enc.Encode(header); err != nil {
		if r.gz != nil {
			_ = r.gz.Close()
		}
		_ = f.Close()
		return nil, fmt.Errorf("writing run log header: %w", err)
	}
	return r, nil
}

// offsetMs converts a simulated timestamp to a millisecond offset from run start.
func (r *runRecorder) offsetMs(ts time.Time) float64 {
	return float64(ts.Sub(r.epoch)) / float64(time.Millisecond)
}

// write encodes one record, holding the lock. Write errors are sticky and
// reported once; later events are dropped rather than spamming stderr.
func (r *runRecorder) write(record any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	if err := r.enc.Encode(record); err != nil {
		r.err = err
		fmt.Fprintf(os.Stderr, "error writing run log: %v; further events are dropped\n", err)
	}
}

func (r *runRecorder) Observe(info synth.SpanInfo) {
	rec := spanRecord{
		Type:            recordTypeSpan,
		T:               r.offsetMs(info.Timestamp),
		D:               float64(info.Duration) / float64(time.Millisecond),
		Service:         info.Service,
		Operation:       info.Operation,
		ParentService:   info.ParentService,
		ParentOperation: info.ParentOperation,
		Error:           info.IsError,
	}
	if sc := info.SpanContext; sc.IsValid() {
		rec.TraceID = sc.TraceID().String()
		rec.SpanID = sc.SpanID().String()
	}
	r.write(rec)
}

func (r *runRecorder) ObservePlanEvent(ev synth.PlanEvent) {
	r.write(planRecord{
		Type:      recordTypePlan,
		T:         r.offsetMs(ev.Timestamp),
		Kind:      ev.Kind,
		Service:   ev.Service,
		Operation: ev.Operation,
	})
}

// finish writes the stats trailer and closes the log. Safe to call with a
// nil stats when the run failed before producing any.
func (r *runRecorder) finish(stats *synth.Stats) error {
	if stats != nil {
		r.write(statsRecord{
			Type:  recordTypeStats,
			T:     float64(stats.ElapsedMs),
			Stats: stats,
		})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	if r.gz != nil {
		errs = append(errs, r.gz.Close())
	}
	errs = append(errs, r.file.Close(), r.err)
	return errors.Join(errs...)
}

// scenarioWindows converts resolved scenarios to header windows.
func scenarioWindows(scenarios []synth.Scenario) []scenarioWindow {
	if len(scenarios) == 0 {
		return nil
	}
	windows := make([]scenarioWindow, len(scenarios))
	for i, sc := range scenarios {
		windows[i] = scenarioWindow{
			Name:    sc.Name,
			StartMs: sc.Start.Milliseconds(),
			EndMs:   sc.End.Milliseconds(),
		}
	}
	return windows
}

// loadRunLog reads and parses a run log written by motel run --graph-record.
func loadRunLog(path string) (*runLog, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied file path is expected
	if err != nil {
		return nil, fmt.Errorf("opening run log: %w", err)
	}
	defer f.Close() //nolint:errcheck // best-effort close on read-only file

	var reader io.Reader = f
	if strings.HasSuffix(path, gzipSuffix) {
		gz, gzErr := gzip.NewReader(f)
		if gzErr != nil {
			return nil, fmt.Errorf("reading compressed run log %s: %w", path, gzErr)
		}
		defer gz.Close() //nolint:errcheck // best-effort close on read
		reader = gz
	}

	dec := json.NewDecoder(reader)
	var log runLog
	sawHeader := false
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("parsing run log %s: %w", path, err)
		}

		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("parsing run log %s: %w", path, err)
		}

		switch probe.Type {
		case recordTypeRun:
			if err := json.Unmarshal(raw, &log.Header); err != nil {
				return nil, fmt.Errorf("parsing run log header: %w", err)
			}
			sawHeader = true
		case recordTypeSpan:
			var rec spanRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				return nil, fmt.Errorf("parsing span record: %w", err)
			}
			log.Spans = append(log.Spans, rec)
		case recordTypePlan:
			var rec planRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				return nil, fmt.Errorf("parsing plan record: %w", err)
			}
			log.Plans = append(log.Plans, rec)
		case recordTypeStats:
			var rec statsRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				return nil, fmt.Errorf("parsing stats record: %w", err)
			}
			log.Stats = &rec
		default:
			return nil, fmt.Errorf("run log %s: unknown record type %q (expected a recording from motel run --graph-record of this motel version)", path, probe.Type)
		}
	}

	if !sawHeader {
		return nil, fmt.Errorf("run log %s contains no run header", path)
	}
	if log.Header.Version > runLogVersion {
		return nil, fmt.Errorf("run log %s has format version %d, newer than this motel supports (%d)", path, log.Header.Version, runLogVersion)
	}
	if len(log.Spans) == 0 {
		return nil, fmt.Errorf("run log %s contains no spans", path)
	}
	return &log, nil
}

// runLogDuration returns the length of a recorded run: the stats trailer's
// elapsed time when present, otherwise the latest span end.
func runLogDuration(log *runLog) time.Duration {
	if log.Stats != nil {
		return time.Duration(log.Stats.T * float64(time.Millisecond))
	}
	maxEnd := 0.0
	for _, rec := range log.Spans {
		maxEnd = max(maxEnd, rec.T+rec.D)
	}
	return time.Duration(maxEnd * float64(time.Millisecond))
}

// defaultReplayInterval is the bucket width when --interval is not given
// and the run is short; longer runs widen automatically.
const defaultReplayInterval = 500 * time.Millisecond

// replayInterval picks a bucket width for a run of the given length,
// mirroring the adaptive sampling used by motel preview.
func replayInterval(total time.Duration) time.Duration {
	switch {
	case total > 30*time.Minute:
		return 5 * time.Second
	case total > 10*time.Minute:
		return 2 * time.Second
	case total > 2*time.Minute:
		return time.Second
	default:
		return defaultReplayInterval
	}
}

// bucketSnapshots aggregates span records into cumulative per-service
// counter snapshots at the given interval, the form the graph viewer
// consumes. Spans are counted in the bucket where they end, matching when
// the live view observes them.
func bucketSnapshots(log *runLog, interval time.Duration) []liveSnapshot {
	type endEvent struct {
		endMs float64
		rec   spanRecord
	}
	events := make([]endEvent, len(log.Spans))
	maxEndMs := 0.0
	for i, rec := range log.Spans {
		end := rec.T + rec.D
		events[i] = endEvent{endMs: end, rec: rec}
		maxEndMs = max(maxEndMs, end)
	}
	slices.SortFunc(events, func(a, b endEvent) int {
		switch {
		case a.endMs < b.endMs:
			return -1
		case a.endMs > b.endMs:
			return 1
		default:
			return 0
		}
	})

	intervalMs := float64(interval) / float64(time.Millisecond)
	buckets := int(maxEndMs/intervalMs) + 1

	totals := map[string]*liveServiceStats{}
	snapshots := make([]liveSnapshot, 0, buckets+1)
	// Baseline snapshot at t=0 with empty counters, so the first bucket's
	// rates derive from a delta against the run start, matching live mode.
	snapshots = append(snapshots, liveSnapshot{
		TimestampMs: log.Header.StartMs,
		Services:    map[string]liveServiceStats{},
	})
	next := 0

	for b := 1; b <= buckets; b++ {
		boundary := float64(b) * intervalMs
		for next < len(events) && events[next].endMs <= boundary {
			rec := events[next].rec
			s, ok := totals[rec.Service]
			if !ok {
				s = &liveServiceStats{}
				totals[rec.Service] = s
			}
			s.Spans++
			if rec.Error {
				s.Errors++
			}
			s.DurationMs += rec.D
			next++
		}
		services := make(map[string]liveServiceStats, len(totals))
		for name, s := range totals {
			services[name] = *s
		}
		snapshots = append(snapshots, liveSnapshot{
			TimestampMs: log.Header.StartMs + int64(boundary),
			Services:    services,
		})
	}
	return snapshots
}
