package pipelinetest

import (
	"bytes"
	"net/http"
	"testing"

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
