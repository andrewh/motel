package synth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/pipelinetest"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
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
	received := waitForSpans(t, sink, sent.Len())

	if err := pipelinetest.CheckConservation(sent, received); err != nil {
		t.Fatal(err)
	}
}

// TestPipeline_AllSpansRoundTripProperty is the property-testing direction the
// epic envisions: for any generated topology, a pass-through pipeline conserves
// every span. One collector is reused across all draws.
func TestPipeline_AllSpansRoundTripProperty(t *testing.T) {
	sink, collector := startPipeline(t, "")

	// Sent identities accumulate rather than resetting the sink between draws:
	// a reset races an in-flight export from the previous draw and can
	// misattribute a late span to the next draw. Over a running set, a late
	// span is still a span that was sent.
	sent := pipelinetest.NewSent()

	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		seed := rapid.Uint64().Draw(t, "seed")
		before := sent.Len()
		addSent(sent, generateAndCapture(t, topo, collector.OTLPEndpoint, 5, seed, nil))
		if sent.Len() == before {
			t.Skip("no spans generated")
		}
		received := waitForSpans(t, sink, sent.Len())

		if err := pipelinetest.CheckConservation(sent, received); err != nil {
			t.Fatal(err)
		}
	})
}

// samplingPipeline drops roughly half of all traces. It is the negative
// control: it proves the harness observes real pipeline behaviour rather than
// echoing what was sent, and previews the sampling invariants of issue 74.
var samplingPipeline = pipelinetest.TracesConfig(`  probabilistic_sampler:
    sampling_percentage: 50
`, "probabilistic_sampler")

// TestPipeline_SamplingDropsSpans confirms the harness detects a pipeline that
// transforms its input: a 50% sampler must keep some spans but not all, and
// everything that survives must be a span that was actually sent.
func TestPipeline_SamplingDropsSpans(t *testing.T) {
	sink, collector := startPipeline(t, samplingPipeline)

	topo := loadTopology(t, passthroughTopology)
	sent := generateAndSend(t, topo, collector.OTLPEndpoint, 40, 7)

	// The sampler drops whole traces, so there is no exact count to wait for.
	spans := sink.WaitSettled(time.Second, 10*time.Second)
	received := pipelinetest.ReceivedKeys(spans)

	if len(received) == 0 || len(received) >= sent.Len() {
		t.Fatalf("expected partial sampling: sent %d, received %d", sent.Len(), len(received))
	}
	if err := pipelinetest.CheckNoFabrication(sent, spans); err != nil {
		t.Fatal(err)
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

// generateAndSend emits n traces from topo into an OTLP exporter pointed at
// the collector and returns the identities sent, as a pipelinetest.Sent.
func generateAndSend(t testingT, topo *Topology, endpoint string, n int, seed uint64) *pipelinetest.Sent {
	t.Helper()
	sent := pipelinetest.NewSent()
	addSent(sent, generateAndCapture(t, topo, endpoint, n, seed, nil))
	return sent
}

// addSent records every captured span's identity into sent.
func addSent(sent *pipelinetest.Sent, spans []tracetest.SpanStub) {
	for _, s := range spans {
		tid := s.SpanContext.TraceID()
		sid := s.SpanContext.SpanID()
		sent.Add(tid[:], sid[:])
	}
}

// generateAndCapture emits n traces from topo into an OTLP exporter pointed at
// the collector via the public GenerateTraces API and returns the spans sent.
// The spans come from a second in-memory exporter attached to the same
// TracerProvider, so the test knows exactly what it pushed, including parent
// relationships. A non-nil idGen overrides the SDK's random trace/span ID
// generator, letting tests replay the exact same span identities.
func generateAndCapture(t testingT, topo *Topology, endpoint string, n int, seed uint64, idGen sdktrace.IDGenerator) []tracetest.SpanStub {
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
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithSyncer(otlp),
		sdktrace.WithSyncer(captured),
	}
	if idGen != nil {
		opts = append(opts, sdktrace.WithIDGenerator(idGen))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	defer func() { _ = tp.Shutdown(ctx) }()

	if _, err := GenerateTraces(ctx, topo, TracerProviderSource(tp), GenerateOptions{Traces: n, Seed: seed}); err != nil {
		t.Fatalf("GenerateTraces: %v", err)
	}
	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}

	return captured.GetSpans()
}

// waitForSpans blocks until the sink holds at least want spans, failing the
// test if that count is not reached within the timeout, and returns every span
// received.
func waitForSpans(t testingT, sink *pipelinetest.Sink, want int) []*tracepb.Span {
	t.Helper()

	if !sink.WaitFor(want, 10*time.Second) {
		t.Fatalf("timed out waiting for %d spans; sink holds %d", want, sink.Count())
	}
	return sink.Spans()
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
