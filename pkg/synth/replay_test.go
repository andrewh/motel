package synth

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// replayObserver counts observer callbacks for assertions.
type replayObserver struct {
	started  int
	observed int
}

func newReplayObserver() *replayObserver { return &replayObserver{} }

func (o *replayObserver) Observe(info SpanInfo)           { o.observed++ }
func (o *replayObserver) ObserveStart(service, op string) { o.started++ }

// noopTracers returns a TracerSource backed by the OTel noop provider.
func noopTracers() TracerSource {
	provider := noop.NewTracerProvider()
	return func(name string) trace.Tracer { return provider.Tracer(name) }
}

func writeRecordingFile(t *testing.T, path string, traces ...RecordedTrace) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create recording: %v", err)
	}
	defer f.Close() //nolint:errcheck // test cleanup
	w := NewRecordingWriter(f)
	for _, tr := range traces {
		if err := w.Write(tr); err != nil {
			t.Fatalf("write recording: %v", err)
		}
	}
}

func sampleRecording() RecordedTrace {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return RecordedTrace{
		TraceID: "trace-1",
		Spans: []RecordedSpan{
			{SpanID: "a", Service: "api", Operation: "GET /x", Start: base, End: base.Add(100 * time.Millisecond)},
			{SpanID: "b", ParentID: "a", Service: "db", Operation: "query", Start: base.Add(10 * time.Millisecond), End: base.Add(60 * time.Millisecond), Error: true, Attributes: map[string]string{"db.system": "pg"}},
		},
	}
}

func TestRecordingRoundTrip(t *testing.T) {
	want := sampleRecording()

	var buf bytes.Buffer
	w := NewRecordingWriter(&buf)
	if err := w.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}

	var got []RecordedTrace
	if err := ReadRecording(&buf, func(tr RecordedTrace) error {
		got = append(got, tr)
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d traces, want 1", len(got))
	}
	if got[0].TraceID != want.TraceID || len(got[0].Spans) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got[0])
	}
	if !got[0].Spans[0].Start.Equal(want.Spans[0].Start) {
		t.Errorf("start time not preserved: got %v want %v", got[0].Spans[0].Start, want.Spans[0].Start)
	}
}

func TestBuildReplayPlansStructure(t *testing.T) {
	plans := buildReplayPlans(sampleRecording(), 0)
	if len(plans) != 2 {
		t.Fatalf("got %d plans, want 2", len(plans))
	}
	// Root first (tree order), child references it.
	if plans[0].ParentIndex != -1 {
		t.Errorf("root ParentIndex = %d, want -1", plans[0].ParentIndex)
	}
	if plans[0].Kind != trace.SpanKindServer {
		t.Errorf("root kind = %v, want Server", plans[0].Kind)
	}
	if plans[1].ParentIndex != 0 {
		t.Errorf("child ParentIndex = %d, want 0", plans[1].ParentIndex)
	}
	if plans[1].Kind != trace.SpanKindClient {
		t.Errorf("child kind = %v, want Client", plans[1].Kind)
	}
	if !plans[1].IsError {
		t.Errorf("child error flag not preserved")
	}
	// Recorded attributes must be carried onto the plan.
	var foundAttr bool
	for _, a := range plans[1].Attrs {
		if string(a.Key) == "db.system" && a.Value.AsString() == "pg" {
			foundAttr = true
		}
	}
	if !foundAttr {
		t.Errorf("recorded attribute db.system=pg not preserved: %v", plans[1].Attrs)
	}
}

func TestBuildReplayPlansShift(t *testing.T) {
	rec := sampleRecording()
	orig := rec.Spans[0].Start
	shift := 48 * time.Hour
	plans := buildReplayPlans(rec, shift)
	if !plans[0].StartTime.Equal(orig.Add(shift)) {
		t.Errorf("shift not applied: got %v want %v", plans[0].StartTime, orig.Add(shift))
	}
	// Relative offsets preserved: child still starts 10ms after root.
	gap := plans[1].StartTime.Sub(plans[0].StartTime)
	if gap != 10*time.Millisecond {
		t.Errorf("intra-trace offset not preserved: got %v want 10ms", gap)
	}
}

func TestBuildReplayPlansClampsSkew(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := RecordedTrace{
		TraceID: "t",
		Spans: []RecordedSpan{
			{SpanID: "p", Service: "s", Operation: "parent", Start: base, End: base.Add(50 * time.Millisecond)},
			// Child appears to start before its parent (clock skew).
			{SpanID: "c", ParentID: "p", Service: "s", Operation: "child", Start: base.Add(-5 * time.Millisecond), End: base.Add(20 * time.Millisecond)},
		},
	}
	plans := buildReplayPlans(rec, 0)
	if plans[1].StartTime.Before(plans[0].StartTime) {
		t.Errorf("child start %v should be clamped to >= parent start %v", plans[1].StartTime, plans[0].StartTime)
	}
}

func TestReplayShiftRelativeVsVerbatim(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	anchor := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)

	rel := ReplayOptions{Start: start, Anchor: anchor}
	if got := rel.shift(); got != anchor.Sub(start) {
		t.Errorf("relative shift = %v, want %v", got, anchor.Sub(start))
	}

	verb := ReplayOptions{Start: start, Anchor: anchor, Verbatim: true}
	if got := verb.shift(); got != 0 {
		t.Errorf("verbatim shift = %v, want 0", got)
	}
}

func TestReplayRecordingEmitsViaTracers(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	writeRecordingFile(t, path, sampleRecording())

	rec := newReplayObserver()
	tracers := noopTracers()

	stats, err := ReplayRecording(context.Background(), path, tracers, []SpanObserver{rec}, ReplayOptions{Verbatim: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if stats.Traces != 1 || stats.Spans != 2 {
		t.Fatalf("stats = %+v, want 1 trace / 2 spans", stats)
	}
	if stats.Errors != 1 {
		t.Errorf("errors = %d, want 1", stats.Errors)
	}
	if rec.started != 2 || rec.observed != 2 {
		t.Errorf("observer fired started=%d observed=%d, want 2/2", rec.started, rec.observed)
	}
}
