package synth

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

func validIDRecording() RecordedTrace {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return RecordedTrace{
		TraceID: "00112233445566778899aabbccddeeff",
		Spans: []RecordedSpan{
			{SpanID: "0102030405060708", Service: "api", Operation: "GET /x", Start: base, End: base.Add(100 * time.Millisecond)},
			{SpanID: "1112131415161718", ParentID: "0102030405060708", Service: "db", Operation: "query", Start: base.Add(10 * time.Millisecond), End: base.Add(60 * time.Millisecond)},
		},
	}
}

func mustBuildReplayPlans(t *testing.T, rec RecordedTrace, shift time.Duration) []SpanPlan {
	t.Helper()
	plans, err := buildReplayPlans(rec, shift, false)
	if err != nil {
		t.Fatalf("buildReplayPlans: %v", err)
	}
	return plans
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
	plans := mustBuildReplayPlans(t, sampleRecording(), 0)
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

// TestBuildReplayPlansSameServiceChildIsInternal pins that replay derives the
// same span kinds as generation: a recorded child on its parent's service is
// INTERNAL, while a cross-service child (or grandchild) is CLIENT.
func TestBuildReplayPlansSameServiceChildIsInternal(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := RecordedTrace{
		TraceID: "trace-internal",
		Spans: []RecordedSpan{
			{SpanID: "a", Service: "checkout", Operation: "submit", Start: base, End: base.Add(100 * time.Millisecond)},
			{SpanID: "b", ParentID: "a", Service: "checkout", Operation: "validate", Start: base.Add(10 * time.Millisecond), End: base.Add(50 * time.Millisecond)},
			{SpanID: "c", ParentID: "b", Service: "inventory", Operation: "reserve", Start: base.Add(20 * time.Millisecond), End: base.Add(40 * time.Millisecond)},
		},
	}

	plans := mustBuildReplayPlans(t, rec, 0)
	if len(plans) != 3 {
		t.Fatalf("got %d plans, want 3", len(plans))
	}
	if plans[0].Kind != trace.SpanKindServer {
		t.Errorf("root kind = %v, want Server", plans[0].Kind)
	}
	if plans[1].Kind != trace.SpanKindInternal {
		t.Errorf("same-service child kind = %v, want Internal", plans[1].Kind)
	}
	if plans[2].Kind != trace.SpanKindClient {
		t.Errorf("cross-service grandchild kind = %v, want Client", plans[2].Kind)
	}
}

func TestBuildReplayPlansShift(t *testing.T) {
	rec := sampleRecording()
	orig := rec.Spans[0].Start
	shift := 48 * time.Hour
	plans := mustBuildReplayPlans(t, rec, shift)
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
	plans := mustBuildReplayPlans(t, rec, 0)
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

func TestReplayRecordingCountsFailedTraceStats(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	rec := sampleRecording()
	rec.Spans[0].Error = true
	writeRecordingFile(t, path, rec)

	stats, err := ReplayRecording(context.Background(), path, noopTracers(), nil, ReplayOptions{Verbatim: true})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if stats.FailedTraces != 1 {
		t.Errorf("failed_traces = %d, want 1", stats.FailedTraces)
	}
	if stats.TraceErrorRate != 1 {
		t.Errorf("trace_error_rate = %v, want 1", stats.TraceErrorRate)
	}
}

func TestReplayRecordingPreservesIDsWhenRequested(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	rec := validIDRecording()
	writeRecordingFile(t, path, rec)

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithIDGenerator(NewReplayIDGenerator()),
	)
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	tracers := func(name string) trace.Tracer {
		return provider.Tracer(name)
	}

	_, err := ReplayRecording(context.Background(), path, tracers, nil, ReplayOptions{
		Verbatim:    true,
		PreserveIDs: true,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	wantTraceID, _ := trace.TraceIDFromHex(rec.TraceID)
	wantRootID, _ := trace.SpanIDFromHex(rec.Spans[0].SpanID)
	wantChildID, _ := trace.SpanIDFromHex(rec.Spans[1].SpanID)

	ended := recorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(ended))
	}
	spans := make(map[string]sdktrace.ReadOnlySpan, len(ended))
	for _, span := range ended {
		spans[span.Name()] = span
	}

	root := spans["GET /x"]
	child := spans["query"]
	if root == nil || child == nil {
		t.Fatalf("missing replayed spans: %v", spans)
	}
	if root.SpanContext().TraceID() != wantTraceID || child.SpanContext().TraceID() != wantTraceID {
		t.Fatalf("trace IDs not preserved: root=%s child=%s want %s",
			root.SpanContext().TraceID(), child.SpanContext().TraceID(), wantTraceID)
	}
	if root.SpanContext().SpanID() != wantRootID {
		t.Errorf("root span ID = %s, want %s", root.SpanContext().SpanID(), wantRootID)
	}
	if child.SpanContext().SpanID() != wantChildID {
		t.Errorf("child span ID = %s, want %s", child.SpanContext().SpanID(), wantChildID)
	}
	if child.Parent().SpanID() != wantRootID {
		t.Errorf("child parent span ID = %s, want %s", child.Parent().SpanID(), wantRootID)
	}
}

func TestReplayRecordingRejectsInvalidPreservedIDs(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/rec.jsonl"
	writeRecordingFile(t, path, sampleRecording())

	_, err := ReplayRecording(context.Background(), path, noopTracers(), nil, ReplayOptions{
		Verbatim:    true,
		PreserveIDs: true,
	})
	if err == nil {
		t.Fatalf("expected invalid ID error")
	}
	if !strings.Contains(err.Error(), "invalid recorded trace ID") {
		t.Fatalf("error = %v, want invalid trace ID", err)
	}
}

// recordingBytes serializes traces into the newline-delimited recording format,
// mirroring an in-memory (non-filesystem) recording such as the WASM playground
// holds.
func recordingBytes(t *testing.T, traces ...RecordedTrace) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewRecordingWriter(&buf)
	for _, tr := range traces {
		if err := w.Write(tr); err != nil {
			t.Fatalf("write recording: %v", err)
		}
	}
	return buf.Bytes()
}

func TestScanRecordingFromCollectsServicesAndStart(t *testing.T) {
	rec := sampleRecording()
	data := recordingBytes(t, rec)

	info, err := ScanRecordingFrom(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if info.Traces != 1 || info.Spans != 2 {
		t.Fatalf("info = %+v, want 1 trace / 2 spans", info)
	}
	if got, want := info.Services, []string{"api", "db"}; !slices.Equal(got, want) {
		t.Errorf("services = %v, want %v", got, want)
	}
	if !info.Start.Equal(rec.Spans[0].Start) {
		t.Errorf("start = %v, want %v", info.Start, rec.Spans[0].Start)
	}
}

func TestReplayRecordingFromEmitsViaTracers(t *testing.T) {
	data := recordingBytes(t, sampleRecording())

	rec := newReplayObserver()
	stats, err := ReplayRecordingFrom(context.Background(), bytes.NewReader(data), noopTracers(), []SpanObserver{rec}, ReplayOptions{Verbatim: true})
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

// TestReaderReplayTwoPass exercises the in-memory flow the reader variants
// exist for: a single recording held as bytes, scanned then replayed by handing
// a fresh reader to each pass, with the scan's Start driving relative shifting.
func TestReaderReplayTwoPass(t *testing.T) {
	rec := sampleRecording()
	data := recordingBytes(t, rec)
	anchor := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)

	info, err := ScanRecordingFrom(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	tracers := func(name string) trace.Tracer { return provider.Tracer(name) }

	stats, err := ReplayRecordingFrom(context.Background(), bytes.NewReader(data), tracers, nil, ReplayOptions{
		Start:  info.Start,
		Anchor: anchor,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if stats.Traces != 1 || stats.Spans != 2 {
		t.Fatalf("stats = %+v, want 1 trace / 2 spans", stats)
	}

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d ended spans, want 2", len(spans))
	}
	// Relative mode maps the earliest recorded start onto the anchor.
	var earliest time.Time
	for _, s := range spans {
		if earliest.IsZero() || s.StartTime().Before(earliest) {
			earliest = s.StartTime()
		}
	}
	if !earliest.Equal(anchor) {
		t.Errorf("earliest replayed start = %v, want anchor %v", earliest, anchor)
	}
}
