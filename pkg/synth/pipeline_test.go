package synth

import (
	"context"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/pipelinetest"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"pgregory.net/rapid"
)

// These tests are the proof-of-concept for issue 73: drive motel through a real
// OpenTelemetry Collector and assert an invariant on the collector's output.
// The trivial invariant is span conservation — a pass-through pipeline must
// emit every span it receives.
//
// Each test skips when no collector binary is available (set MOTEL_COLLECTOR_BIN
// or put otelcol on PATH), so the suite stays green without one.

const passthroughTopology = `version: 1
services:
  gateway:
    operations:
      handle:
        duration: 5ms +/- 1ms
        calls:
          - backend.read
  backend:
    operations:
      read:
        duration: 2ms +/- 1ms
        calls:
          - db.query
  db:
    operations:
      query:
        duration: 1ms +/- 0.5ms
traffic:
  rate: 10/s
`

// TestPipeline_AllSpansRoundTrip generates a fixed topology's traces through a
// pass-through collector and asserts every span sent arrives at the sink.
func TestPipeline_AllSpansRoundTrip(t *testing.T) {
	sink, collector := startPipeline(t, "")

	topo := loadTopology(t, passthroughTopology)
	sent := generateAndSend(t, topo, collector.OTLPEndpoint, 20, 1)
	received := waitForSpans(t, sink, len(sent))

	assertSameSpans(t, sent, received)
}

// TestPipeline_AllSpansRoundTripProperty is the property-testing direction the
// epic envisions: for any generated topology, a pass-through pipeline conserves
// every span. One collector is reused across all draws.
func TestPipeline_AllSpansRoundTripProperty(t *testing.T) {
	sink, collector := startPipeline(t, "")

	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		sink.Reset()
		seed := rapid.Uint64().Draw(t, "seed")
		sent := generateAndSend(t, topo, collector.OTLPEndpoint, 5, seed)
		if len(sent) == 0 {
			t.Skip("no spans generated")
		}
		received := waitForSpans(t, sink, len(sent))

		assertSameSpans(t, sent, received)
	})
}

// samplingPipeline drops roughly half of all traces. It is the negative
// control: it proves the harness observes real pipeline behaviour rather than
// echoing what was sent, and previews the sampling invariants of issue 74.
const samplingPipeline = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:{{.OTLPHTTPPort}}
processors:
  probabilistic_sampler:
    sampling_percentage: 50
exporters:
  otlphttp:
    endpoint: {{.SinkURL}}
    compression: none
extensions:
  health_check:
    endpoint: 127.0.0.1:{{.HealthPort}}
service:
  extensions: [health_check]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [probabilistic_sampler]
      exporters: [otlphttp]
  telemetry:
    metrics:
      level: none
    logs:
      level: warn
`

// TestPipeline_SamplingDropsSpans confirms the harness detects a pipeline that
// transforms its input: a 50% sampler must keep some spans but not all, and
// everything that survives must be a span that was actually sent.
func TestPipeline_SamplingDropsSpans(t *testing.T) {
	sink, collector := startPipeline(t, samplingPipeline)

	topo := loadTopology(t, passthroughTopology)
	sent := generateAndSend(t, topo, collector.OTLPEndpoint, 40, 7)

	// The sampler drops whole traces, so the sent count is never reached.
	// Let the pipeline settle, then snapshot what survived.
	time.Sleep(2 * time.Second)
	received := make(map[string]struct{})
	for _, s := range sink.Spans() {
		received[spanKey(s.GetTraceId(), s.GetSpanId())] = struct{}{}
	}

	if len(received) == 0 || len(received) >= len(sent) {
		t.Fatalf("expected partial sampling: sent %d, received %d", len(sent), len(received))
	}
	for key := range received {
		if _, ok := sent[key]; !ok {
			t.Fatalf("received span %s that was never sent", key)
		}
	}
}

// startPipeline starts a sink and a collector wired to it, registering cleanup.
// It skips the test when no collector binary is available.
func startPipeline(t testing.TB, config string) (*pipelinetest.Sink, *pipelinetest.Collector) {
	t.Helper()
	if _, ok := pipelinetest.CollectorBinary(); !ok {
		t.Skipf("no collector binary (set %s or install otelcol)", pipelinetest.BinaryEnv)
	}

	sink := pipelinetest.NewSink()
	t.Cleanup(sink.Close)

	collector, err := pipelinetest.Start(sink, config)
	if errors.Is(err, pipelinetest.ErrNoCollector) {
		sink.Close()
		t.Skip("collector binary disappeared")
	}
	if err != nil {
		sink.Close()
		t.Fatalf("start collector: %v", err)
	}
	t.Cleanup(func() { _ = collector.Stop() })

	return sink, collector
}

// testingT is the slice of testing.TB the round-trip helpers need. Both
// *testing.T and *rapid.T satisfy it, so the helpers work in plain tests and
// inside rapid.Check.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// generateAndSend walks n traces from topo into an OTLP exporter pointed at the
// collector and returns the set of span identities sent. Identities come from a
// second in-memory exporter so the test knows exactly what it pushed.
func generateAndSend(t testingT, topo *Topology, endpoint string, n int, seed uint64) map[string]struct{} {
	t.Helper()

	ctx := context.Background()
	otlp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		t.Fatalf("otlptracehttp.New: %v", err)
	}
	captured := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(otlp),
		sdktrace.WithSyncer(captured),
	)
	defer func() { _ = tp.Shutdown(ctx) }()

	for i := range n {
		rng := rand.New(rand.NewPCG(seed+uint64(i), 0)) //nolint:gosec // not security-sensitive
		engine := &Engine{
			Topology: topo,
			Tracers:  func(string) trace.Tracer { return tp.Tracer("github.com/andrewh/motel") },
			Rng:      rng,
		}
		root := topo.Roots[rng.IntN(len(topo.Roots))]
		var stats Stats
		engine.walkTrace(ctx, root, nil, time.Now(), 0, nil, nil, &stats, new(int), DefaultMaxSpansPerTrace, false, false)
	}
	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}

	sent := make(map[string]struct{})
	for _, s := range captured.GetSpans() {
		tid := s.SpanContext.TraceID()
		sid := s.SpanContext.SpanID()
		sent[spanKey(tid[:], sid[:])] = struct{}{}
	}
	return sent
}

// waitForSpans polls the sink until it holds at least want spans or a timeout
// elapses, then returns the identities of everything received.
func waitForSpans(t testingT, sink *pipelinetest.Sink, want int) map[string]struct{} {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && sink.Count() < want {
		time.Sleep(20 * time.Millisecond)
	}

	received := make(map[string]struct{})
	for _, s := range sink.Spans() {
		received[spanKey(s.GetTraceId(), s.GetSpanId())] = struct{}{}
	}
	return received
}

// assertSameSpans checks that the received identities are exactly the sent set.
func assertSameSpans(t testingT, sent, received map[string]struct{}) {
	t.Helper()

	if len(received) != len(sent) {
		t.Fatalf("span count mismatch: sent %d, received %d", len(sent), len(received))
	}
	for key := range sent {
		if _, ok := received[key]; !ok {
			t.Fatalf("span %s sent but not received", key)
		}
	}
}

func spanKey(traceID, spanID []byte) string {
	return hex.EncodeToString(traceID) + ":" + hex.EncodeToString(spanID)
}

// loadTopology writes a topology to a temp file and builds it.
func loadTopology(t testing.TB, yaml string) *Topology {
	t.Helper()

	path := filepath.Join(t.TempDir(), "topology.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write topology: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	topo, err := BuildTopology(cfg)
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	return topo
}
