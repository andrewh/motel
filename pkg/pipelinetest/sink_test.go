package pipelinetest

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestSink_CapturesExportedSpans posts an OTLP/HTTP export request to the sink
// and checks every span is captured. This exercises the decode path without a
// collector, so it runs everywhere.
func TestSink_CapturesExportedSpans(t *testing.T) {
	sink := NewSink()
	defer sink.Close()

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{
					{TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{2}, 8), Name: "a"},
					{TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{3}, 8), Name: "b"},
				},
			}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp, err := http.Post(sink.URL()+"/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	if got := sink.Count(); got != 2 {
		t.Fatalf("Count: got %d, want 2", got)
	}
	names := map[string]bool{}
	for _, s := range sink.Spans() {
		names[s.GetName()] = true
	}
	if !names["a"] || !names["b"] {
		t.Fatalf("missing spans: got %v", names)
	}

	sink.Reset()
	if got := sink.Count(); got != 0 {
		t.Fatalf("Count after Reset: got %d, want 0", got)
	}
}

// postSpans exports count spans to the sink over OTLP/HTTP.
func postSpans(t *testing.T, sink *Sink, count int) {
	t.Helper()
	spans := make([]*tracepb.Span, count)
	for i := range count {
		spans[i] = &tracepb.Span{
			TraceId: bytes.Repeat([]byte{1}, 16),
			SpanId:  append(bytes.Repeat([]byte{0}, 7), byte(i+1)),
		}
	}
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(sink.URL()+"/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
}

// TestSink_WaitFor checks the exact-count wait succeeds once enough spans
// arrive and reports failure when the count is never reached.
func TestSink_WaitFor(t *testing.T) {
	sink := NewSink()
	defer sink.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(50 * time.Millisecond)
		postSpans(t, sink, 3)
	}()

	if !sink.WaitFor(3, 5*time.Second) {
		t.Fatalf("WaitFor(3): count %d never reached 3", sink.Count())
	}
	<-done

	if sink.WaitFor(4, 100*time.Millisecond) {
		t.Fatal("WaitFor(4) succeeded with only 3 spans received")
	}
}

// TestSink_WaitSettled checks the bounded "eventually received" wait: it
// returns everything that arrived once the sink goes quiet, and it returns
// promptly (bounded by max, not hanging) when nothing arrives at all.
func TestSink_WaitSettled(t *testing.T) {
	sink := NewSink()
	defer sink.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		postSpans(t, sink, 2)
		time.Sleep(50 * time.Millisecond)
		postSpans(t, sink, 2)
	}()

	spans := sink.WaitSettled(500*time.Millisecond, 10*time.Second)
	<-done
	if len(spans) != 4 {
		t.Fatalf("WaitSettled: got %d spans, want 4", len(spans))
	}

	sink.Reset()
	start := time.Now()
	if spans := sink.WaitSettled(100*time.Millisecond, time.Second); len(spans) != 0 {
		t.Fatalf("WaitSettled on idle sink: got %d spans, want 0", len(spans))
	}
	if elapsed := time.Since(start); elapsed > time.Second+500*time.Millisecond {
		t.Fatalf("WaitSettled did not respect max: took %v", elapsed)
	}
}
