// Package pipelinetest provides primitives for testing OTLP pipelines: an
// in-process OTLP/HTTP sink that captures exported spans, and a
// subprocess-managed OpenTelemetry Collector. Tests generate signals with
// motel, push them through a real pipeline, and assert invariants on what the
// sink receives.
//
// The package depends only on the OTLP protocol, not on any particular
// pipeline implementation. Anything that exports OTLP/HTTP can be the system
// under test; the OpenTelemetry Collector is the reference target.
package pipelinetest

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Sink is an in-process OTLP/HTTP trace receiver that records every span it
// receives. A pipeline under test exports to Sink.URL; tests assert on the
// captured spans.
type Sink struct {
	server *httptest.Server
	mu     sync.Mutex
	spans  []*tracepb.Span
}

// NewSink starts a Sink listening on an ephemeral loopback port.
// Call Close to stop it.
func NewSink() *Sink {
	s := &Sink{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", s.handleTraces)
	s.server = httptest.NewServer(mux)
	return s
}

// URL is the base endpoint for an OTLP/HTTP exporter. The exporter appends
// /v1/traces to it.
func (s *Sink) URL() string { return s.server.URL }

// Close stops the sink's HTTP server.
func (s *Sink) Close() { s.server.Close() }

func (s *Sink) handleTraces(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			s.spans = append(s.spans, ss.GetSpans()...)
		}
	}
	s.mu.Unlock()

	resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(resp)
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		return io.ReadAll(gz)
	}
	return io.ReadAll(r.Body)
}

// Spans returns a copy of all spans received so far.
func (s *Sink) Spans() []*tracepb.Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*tracepb.Span, len(s.spans))
	copy(out, s.spans)
	return out
}

// Count returns the number of spans received so far.
func (s *Sink) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.spans)
}

// Reset discards all received spans, readying the sink for reuse.
func (s *Sink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = nil
}
