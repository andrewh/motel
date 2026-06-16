// Replay mode: deterministically re-emit pre-recorded trace data.
//
// Unlike the generative engine (which samples per-operation distributions),
// replay reads a sidecar "recording" of real traces and emits them as-is. The
// recording is newline-delimited JSON (one trace per line) so that arbitrarily
// large captures can be written and read incrementally without loading the
// whole file into memory.
//
// PR 1 scope and known limitations (tracked as follow-ups):
//   - Non-realtime only: spans are emitted immediately with their recorded
//     timestamps (relative-shifted or verbatim). Wall-clock-paced replay is a
//     follow-up.
//   - Span kind is derived from tree position (root -> SERVER, else CLIENT)
//     because the importer does not yet capture source span kind.
package synth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// RecordedSpan is one span in a recording. Field names are kept short and
// stable because they are repeated on every span of every trace.
type RecordedSpan struct {
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Service    string            `json:"service"`
	Operation  string            `json:"operation"`
	Start      time.Time         `json:"start"`
	End        time.Time         `json:"end"`
	Error      bool              `json:"error,omitempty"`
	Kind       string            `json:"kind,omitempty"` // reserved; derived on replay when empty
	Attributes map[string]string `json:"attributes,omitempty"`
}

// RecordedTrace is a single trace: a set of spans sharing one trace ID.
type RecordedTrace struct {
	TraceID string         `json:"trace_id"`
	Spans   []RecordedSpan `json:"spans"`
}

// RecordingWriter streams recorded traces to an io.Writer as newline-delimited
// JSON. Callers write one trace at a time, keeping memory bounded.
type RecordingWriter struct {
	enc *json.Encoder
}

// NewRecordingWriter returns a streaming recording writer.
func NewRecordingWriter(w io.Writer) *RecordingWriter {
	return &RecordingWriter{enc: json.NewEncoder(w)}
}

// Write appends one trace to the recording. json.Encoder terminates each
// value with a newline, yielding the newline-delimited format.
func (w *RecordingWriter) Write(t RecordedTrace) error {
	return w.enc.Encode(t)
}

// ReadRecording streams traces from a newline-delimited recording, invoking
// yield for each. Iteration stops on the first yield error or read error.
func ReadRecording(r io.Reader, yield func(RecordedTrace) error) error {
	dec := json.NewDecoder(bufio.NewReader(r))
	for {
		var t RecordedTrace
		if err := dec.Decode(&t); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("reading recording: %w", err)
		}
		if err := yield(t); err != nil {
			return err
		}
	}
}

// RecordingInfo summarises a recording without holding its traces in memory.
type RecordingInfo struct {
	Services []string  // distinct service names, sorted
	Start    time.Time // earliest span start across all traces
	Traces   int
	Spans    int
}

// ScanRecording reads a recording once to collect the data needed to set up
// replay: the set of services (for provider/resource creation) and the global
// earliest start time (for relative time-shifting).
func ScanRecording(path string) (RecordingInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return RecordingInfo{}, fmt.Errorf("opening recording: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file, close error is not actionable

	seen := make(map[string]struct{})
	info := RecordingInfo{}
	err = ReadRecording(f, func(t RecordedTrace) error {
		info.Traces++
		for _, s := range t.Spans {
			info.Spans++
			seen[s.Service] = struct{}{}
			if info.Start.IsZero() || s.Start.Before(info.Start) {
				info.Start = s.Start
			}
		}
		return nil
	})
	if err != nil {
		return RecordingInfo{}, err
	}
	for svc := range seen {
		info.Services = append(info.Services, svc)
	}
	slices.Sort(info.Services)
	return info, nil
}

// ReplayOptions controls how a recording is re-emitted.
type ReplayOptions struct {
	// Verbatim emits spans with their original recorded timestamps. When false
	// (the default), timestamps are shifted so the recording's earliest span
	// starts at the anchor time, preserving all intra- and inter-trace offsets.
	Verbatim bool
	// Anchor is the wall-clock time the earliest span maps to in relative mode.
	// Ignored when Verbatim is true. Zero means time.Now().
	Anchor time.Time
	// Start is the recording's earliest span start (from ScanRecording), used as
	// the relative-mode shift origin. Ignored when Verbatim is true.
	Start time.Time
}

// shift returns the duration added to every recorded timestamp.
func (o ReplayOptions) shift() time.Duration {
	if o.Verbatim || o.Start.IsZero() {
		return 0
	}
	anchor := o.Anchor
	if anchor.IsZero() {
		anchor = time.Now()
	}
	return anchor.Sub(o.Start)
}

// ReplayRecording streams the recording at path and emits each trace through
// the given tracers and observers. Timestamps follow opts. Returns aggregate
// emission statistics.
func ReplayRecording(ctx context.Context, path string, tracers TracerSource, observers []SpanObserver, opts ReplayOptions) (*Stats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening recording: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file, close error is not actionable

	shift := opts.shift()
	var stats Stats
	var rstats realtimeStats
	start := time.Now()

	err = ReadRecording(f, func(t RecordedTrace) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		plans := buildReplayPlans(t, shift)
		if len(plans) == 0 {
			return nil
		}
		emitTraceInstant(plans, tracers, observers, &rstats)
		stats.Traces++
		return nil
	})
	if err != nil && ctx.Err() == nil {
		return nil, err
	}

	stats.Spans = rstats.Spans.Load()
	stats.Errors = rstats.Errors.Load()
	e := &Engine{}
	e.finaliseStats(&stats, start)
	return &stats, nil
}

// buildReplayPlans converts a recorded trace into ordered SpanPlans. Spans are
// indexed in tree order (parents before children) so emission can resolve the
// parent context, and each child's window is clamped to sit within its parent
// to preserve structure even when the source has clock skew.
func buildReplayPlans(t RecordedTrace, shift time.Duration) []SpanPlan {
	byID := make(map[string]*RecordedSpan, len(t.Spans))
	for i := range t.Spans {
		byID[t.Spans[i].SpanID] = &t.Spans[i]
	}

	// Group children by parent; identify roots (no parent, or dangling parent).
	children := make(map[string][]*RecordedSpan)
	var roots []*RecordedSpan
	for i := range t.Spans {
		s := &t.Spans[i]
		if _, ok := byID[s.ParentID]; s.ParentID == "" || !ok {
			roots = append(roots, s)
		} else {
			children[s.ParentID] = append(children[s.ParentID], s)
		}
	}

	byStart := func(a, b *RecordedSpan) int { return a.Start.Compare(b.Start) }
	slices.SortFunc(roots, byStart)

	plans := make([]SpanPlan, 0, len(t.Spans))

	var add func(s *RecordedSpan, parentIndex int, parentStart time.Time)
	add = func(s *RecordedSpan, parentIndex int, parentStart time.Time) {
		startT := s.Start.Add(shift)
		if parentIndex >= 0 && startT.Before(parentStart) {
			startT = parentStart
		}
		endT := s.End.Add(shift)
		if endT.Before(startT) {
			endT = startT
		}

		kind := trace.SpanKindClient
		if parentIndex < 0 {
			kind = trace.SpanKindServer
		}

		attrs := make([]attribute.KeyValue, 0, len(s.Attributes))
		for k, v := range s.Attributes {
			attrs = append(attrs, attribute.String(k, v))
		}

		idx := len(plans)
		plans = append(plans, SpanPlan{
			Index:       idx,
			ParentIndex: parentIndex,
			Service:     s.Service,
			Operation:   s.Operation,
			Ref:         s.Service + "." + s.Operation,
			Kind:        kind,
			StartTime:   startT,
			EndTime:     endT,
			IsError:     s.Error,
			Attrs:       attrs,
		})

		kids := children[s.SpanID]
		slices.SortFunc(kids, byStart)
		for _, c := range kids {
			add(c, idx, startT)
		}
	}

	for _, r := range roots {
		add(r, -1, time.Time{})
	}
	return plans
}

// emitTraceInstant emits a planned trace immediately (no wall-clock pacing),
// stamping each span with its planned timestamps. It mirrors emitTrace's
// Start/End ordering via buildEvents but skips the scheduling timer.
func emitTraceInstant(plans []SpanPlan, tracers TracerSource, observers []SpanObserver, rstats *realtimeStats) {
	if len(plans) == 0 {
		return
	}
	events := buildEvents(plans)
	live := make([]liveSpan, len(plans))

	for _, ev := range events {
		plan := &plans[ev.Index]
		if !ev.IsEnd {
			parentCtx := context.Background()
			if plan.ParentIndex >= 0 && live[plan.ParentIndex].Ctx != nil {
				parentCtx = live[plan.ParentIndex].Ctx
			}
			startOpts := []trace.SpanStartOption{
				trace.WithTimestamp(plan.StartTime),
				trace.WithSpanKind(plan.Kind),
				trace.WithAttributes(plan.StartAttrs...),
			}
			tracer := tracers(plan.Service)
			spanCtx, span := tracer.Start(parentCtx, plan.Operation, startOpts...)
			if len(plan.Attrs) > 0 {
				span.SetAttributes(plan.Attrs...)
			}
			notifySpanStart(observers, plan.Service, plan.Operation)
			live[ev.Index] = liveSpan{Span: span, Ctx: spanCtx}
		} else {
			ls := live[ev.Index]
			if ls.Span == nil {
				continue
			}
			finishSpan(ls.Span, plan, plans, observers, rstats)
			live[ev.Index] = liveSpan{}
		}
	}
}
