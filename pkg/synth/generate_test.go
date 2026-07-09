package synth

import (
	"context"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// generateTestChain builds a three-operation chain across three services:
// gateway.handle -> backend.read -> db.query. Every trace emits exactly
// three spans because no calls are probabilistic or conditional.
func generateTestChain() *Topology {
	gateway := &Service{Name: "gateway", Operations: make(map[string]*Operation)}
	backend := &Service{Name: "backend", Operations: make(map[string]*Operation)}
	db := &Service{Name: "db", Operations: make(map[string]*Operation)}

	query := &Operation{Service: db, Name: "query", Ref: "db.query",
		Duration: Distribution{Mean: time.Millisecond}}
	read := &Operation{Service: backend, Name: "read", Ref: "backend.read",
		Duration: Distribution{Mean: 2 * time.Millisecond},
		Calls:    []Call{{Operation: query}}}
	handle := &Operation{Service: gateway, Name: "handle", Ref: "gateway.handle",
		Duration: Distribution{Mean: 5 * time.Millisecond},
		Calls:    []Call{{Operation: read}}}

	gateway.Operations["handle"] = handle
	backend.Operations["read"] = read
	db.Operations["query"] = query

	return &Topology{
		Services: map[string]*Service{"gateway": gateway, "backend": backend, "db": db},
		Roots:    []*Operation{handle},
	}
}

// newCapturingProvider returns an in-memory exporter and a TracerProvider
// that writes every span to it synchronously.
func newCapturingProvider(t *testing.T) (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return exporter, tp
}

func TestGenerateTraces_EmitsExpectedSpans(t *testing.T) {
	topo := generateTestChain()
	exporter, tp := newCapturingProvider(t)

	const n = 5
	stats, err := GenerateTraces(context.Background(), topo, TracerProviderSource(tp), GenerateOptions{Traces: n, Seed: 42})
	if err != nil {
		t.Fatalf("GenerateTraces: %v", err)
	}

	if stats.Traces != n {
		t.Fatalf("expected %d traces, got %d", n, stats.Traces)
	}
	if stats.Spans != 3*n {
		t.Fatalf("expected %d spans in stats, got %d", 3*n, stats.Spans)
	}

	spans := exporter.GetSpans()
	if len(spans) != 3*n {
		t.Fatalf("expected %d exported spans, got %d", 3*n, len(spans))
	}

	scopeByName := map[string]string{"handle": "gateway", "read": "backend", "query": "db"}
	counts := make(map[string]int)
	for _, s := range spans {
		counts[s.Name]++
		if want := scopeByName[s.Name]; s.InstrumentationScope.Name != want {
			t.Fatalf("span %q has scope %q, want %q", s.Name, s.InstrumentationScope.Name, want)
		}
	}
	for name := range scopeByName {
		if counts[name] != n {
			t.Fatalf("expected %d %q spans, got %d", n, name, counts[name])
		}
	}
}

func TestGenerateTraces_NotifiesObservers(t *testing.T) {
	topo := generateTestChain()
	_, tp := newCapturingProvider(t)

	obs := &recordingObserver{}
	const n = 3
	stats, err := GenerateTraces(context.Background(), topo, TracerProviderSource(tp),
		GenerateOptions{Traces: n, Seed: 42, Observers: []SpanObserver{obs}})
	if err != nil {
		t.Fatalf("GenerateTraces: %v", err)
	}

	records := obs.get()
	// The chain emits exactly three spans per trace, one Observe call each.
	if int64(len(records)) != stats.Spans {
		t.Fatalf("expected one SpanInfo per span (%d), got %d", stats.Spans, len(records))
	}
	if len(records) != 3*n {
		t.Fatalf("expected %d SpanInfo records, got %d", 3*n, len(records))
	}

	// Verify parent attribution for the cross-service call backend.read,
	// whose parent is gateway.handle.
	var read SpanInfo
	found := false
	for _, r := range records {
		if r.Service == "backend" && r.Operation == "read" {
			read = r
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no SpanInfo recorded for backend.read")
	}
	if read.ParentService != "gateway" || read.ParentOperation != "handle" {
		t.Fatalf("backend.read parent = %q/%q, want gateway/handle",
			read.ParentService, read.ParentOperation)
	}

	// Root spans (gateway.handle) carry no parent attribution.
	for _, r := range records {
		if r.Service == "gateway" && r.Operation == "handle" {
			if r.ParentService != "" || r.ParentOperation != "" {
				t.Fatalf("root gateway.handle has parent %q/%q, want empty",
					r.ParentService, r.ParentOperation)
			}
		}
	}
}

func TestGenerateTraces_ReproducibleWithSeed(t *testing.T) {
	run := func() (*Stats, []tracetest.SpanStub) {
		topo := generateTestChain()
		topo.Services["backend"].Operations["read"].ErrorRate = 0.5
		exporter, tp := newCapturingProvider(t)
		stats, err := GenerateTraces(context.Background(), topo, TracerProviderSource(tp), GenerateOptions{Traces: 20, Seed: 7})
		if err != nil {
			t.Fatalf("GenerateTraces: %v", err)
		}
		return stats, exporter.GetSpans()
	}

	first, firstSpans := run()
	second, secondSpans := run()

	if first.Spans != second.Spans || first.Errors != second.Errors || first.FailedTraces != second.FailedTraces {
		t.Fatalf("same seed produced different stats: %+v vs %+v", first, second)
	}
	if len(firstSpans) != len(secondSpans) {
		t.Fatalf("same seed produced %d vs %d spans", len(firstSpans), len(secondSpans))
	}
	for i := range firstSpans {
		if firstSpans[i].Name != secondSpans[i].Name {
			t.Fatalf("span %d name mismatch: %q vs %q", i, firstSpans[i].Name, secondSpans[i].Name)
		}
		if firstSpans[i].Status.Code != secondSpans[i].Status.Code {
			t.Fatalf("span %d status mismatch: %v vs %v", i, firstSpans[i].Status.Code, secondSpans[i].Status.Code)
		}
	}
}

func TestGenerateTraces_BoundsSpansPerTrace(t *testing.T) {
	// root fans out to A three times and each A calls B, so an unbounded
	// trace emits 7 spans; a limit of 5 must cap every trace.
	s := &Service{Name: "s", Operations: make(map[string]*Operation)}
	opB := &Operation{Service: s, Name: "B", Ref: "s.B",
		Duration: Distribution{Mean: time.Millisecond}}
	opA := &Operation{Service: s, Name: "A", Ref: "s.A",
		Duration: Distribution{Mean: time.Millisecond},
		Calls:    []Call{{Operation: opB}}}
	root := &Operation{Service: s, Name: "root", Ref: "s.root",
		Duration: Distribution{Mean: time.Millisecond},
		Calls:    []Call{{Operation: opA, Count: 3}}}
	s.Operations["root"] = root
	s.Operations["A"] = opA
	s.Operations["B"] = opB
	topo := &Topology{Services: map[string]*Service{"s": s}, Roots: []*Operation{root}}

	exporter, tp := newCapturingProvider(t)

	const n = 4
	stats, err := GenerateTraces(context.Background(), topo, TracerProviderSource(tp), GenerateOptions{Traces: n, Seed: 42, MaxSpansPerTrace: 5})
	if err != nil {
		t.Fatalf("GenerateTraces: %v", err)
	}

	if got := len(exporter.GetSpans()); got != 5*n {
		t.Fatalf("expected %d spans with cap 5, got %d", 5*n, got)
	}
	if stats.SpansBounded != n {
		t.Fatalf("expected %d bounded traces, got %d", n, stats.SpansBounded)
	}
}

func TestGenerateTraces_NoRoots(t *testing.T) {
	topo := &Topology{Services: map[string]*Service{}}
	_, tp := newCapturingProvider(t)

	if _, err := GenerateTraces(context.Background(), topo, TracerProviderSource(tp), GenerateOptions{Traces: 1}); err == nil {
		t.Fatal("expected error for topology without roots")
	}
}

func TestGenerateTraces_NilTracerSource(t *testing.T) {
	if _, err := GenerateTraces(context.Background(), generateTestChain(), nil, GenerateOptions{Traces: 1}); err == nil {
		t.Fatal("expected error for nil tracer source")
	}
}

func TestGenerateTraces_NilTopology(t *testing.T) {
	_, tp := newCapturingProvider(t)

	if _, err := GenerateTraces(context.Background(), nil, TracerProviderSource(tp), GenerateOptions{Traces: 1}); err == nil {
		t.Fatal("expected error for nil topology")
	}
}

func TestTracerProviderSource_NilProvider(t *testing.T) {
	if src := TracerProviderSource(nil); src != nil {
		t.Fatal("expected nil TracerSource for nil provider")
	}

	if _, err := GenerateTraces(context.Background(), generateTestChain(), TracerProviderSource(nil), GenerateOptions{Traces: 1}); err == nil {
		t.Fatal("expected error when generating with a nil provider source")
	}
}

func TestGenerateTraces_ZeroTraces(t *testing.T) {
	exporter, tp := newCapturingProvider(t)

	stats, err := GenerateTraces(context.Background(), generateTestChain(), TracerProviderSource(tp), GenerateOptions{})
	if err != nil {
		t.Fatalf("GenerateTraces: %v", err)
	}
	if stats.Traces != 0 || stats.Spans != 0 {
		t.Fatalf("expected zero stats, got %+v", stats)
	}
	if got := len(exporter.GetSpans()); got != 0 {
		t.Fatalf("expected no spans, got %d", got)
	}
}

func TestGenerateTraces_CancelledContext(t *testing.T) {
	_, tp := newCapturingProvider(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stats, err := GenerateTraces(ctx, generateTestChain(), TracerProviderSource(tp), GenerateOptions{Traces: 10, Seed: 42})
	if err == nil {
		t.Fatal("expected context error")
	}
	if stats.Traces != 0 {
		t.Fatalf("expected no traces after immediate cancellation, got %d", stats.Traces)
	}
}
