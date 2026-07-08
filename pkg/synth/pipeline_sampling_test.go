package synth

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/pipelinetest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"pgregory.net/rapid"
)

// Sampling trace integrity invariants (issue 74). Sampling processors decide
// which traces survive a pipeline; these tests assert the decisions never
// break trace structure:
//
//   - Parent-child preservation: if a child span is kept, its parent is kept
//     (no orphaned spans) — assertParentsKept.
//   - Trace completeness: if any span from a trace is kept, all spans from
//     that trace are kept — assertWholeTraces.
//   - Sampling consistency: the same trace replayed through the same sampler
//     gets the same keep/drop decision — TestSampling_DeterministicDecisions.
//
// Like the round-trip tests in pipeline_test.go, they drive a real collector
// subprocess through pkg/pipelinetest and skip when no binary is available.

const (
	// samplerSettleIdle is how long the sink must stay quiet before a head
	// sampler's output is considered complete. The sampler decides
	// synchronously in the receiver path, so only exporter queue drain
	// remains once generation returns.
	samplerSettleIdle = 500 * time.Millisecond

	// propertySettleIdle is the per-draw settle window for the rapid
	// property, kept short because it is paid on every draw.
	propertySettleIdle = 300 * time.Millisecond

	// tailSettleIdle must exceed the tail sampler's decision_wait: the
	// pipeline is silent while traces sit in the decision buffer, and a
	// shorter idle window would mistake that silence for a drained pipeline.
	tailSettleIdle = 2 * time.Second

	settleMax = 30 * time.Second
)

// headSamplerPipeline samples 50% of traces at the head. hash_seed mode makes
// the keep/drop decision a pure function of the trace ID, so it is
// deterministic across runs and identical for every span of a trace.
const headSamplerPipeline = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:{{.OTLPHTTPPort}}
processors:
  probabilistic_sampler:
    sampling_percentage: 50
    mode: hash_seed
    hash_seed: 22
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

// tailSamplingPipeline buffers whole traces for decision_wait, then keeps 50%
// of them. The tail_sampling processor ships in otelcol-contrib, not the
// reference otelcol build, so the test using it checks SupportsComponent.
const tailSamplingPipeline = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:{{.OTLPHTTPPort}}
processors:
  tail_sampling:
    decision_wait: 1s
    policies:
      - name: keep-half
        type: probabilistic
        probabilistic:
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
      processors: [tail_sampling]
      exporters: [otlphttp]
  telemetry:
    metrics:
      level: none
    logs:
      level: warn
`

// TestSampling_TraceIntegrity drives a fixed multi-service topology through a
// 50% head sampler and asserts all structural invariants at once: the
// survivors are a subset of what was sent, no kept span is orphaned, and
// every kept trace is complete.
func TestSampling_TraceIntegrity(t *testing.T) {
	sink, collector := startPipeline(t, headSamplerPipeline)

	topo := loadTopology(t, passthroughTopology)
	sent := generateAndCapture(t, topo, collector.OTLPEndpoint, 40, 7, nil)
	spans := sink.WaitSettled(samplerSettleIdle, settleMax)
	received := receivedKeys(spans)

	if len(received) == 0 || len(received) >= len(sent) {
		t.Fatalf("expected partial sampling: sent %d, received %d", len(sent), len(received))
	}
	assertNoFabrication(t, sentKeys(sent), received)
	assertParentsKept(t, spans)
	assertWholeTraces(t, sent, received)
}

// TestSampling_TraceIntegrityProperty asserts the same invariants for any
// generated topology: whatever the trace shape — depth, fan-out, error mix —
// the sampler must keep or drop traces whole. One collector is reused across
// all draws.
func TestSampling_TraceIntegrityProperty(t *testing.T) {
	sink, collector := startPipeline(t, headSamplerPipeline)

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
		sent := generateAndCapture(t, topo, collector.OTLPEndpoint, 5, seed, nil)
		if len(sent) == 0 {
			t.Skip("no spans generated")
		}
		spans := sink.WaitSettled(propertySettleIdle, settleMax)
		received := receivedKeys(spans)

		assertNoFabrication(t, sentKeys(sent), received)
		assertParentsKept(t, spans)
		assertWholeTraces(t, sent, received)
	})
}

// TestSampling_DeterministicDecisions replays the identical span set through
// the same sampler twice and asserts the keep/drop decisions match. A seeded
// ID generator makes both runs use the same trace and span IDs, which is what
// a hash_seed sampler decides on.
func TestSampling_DeterministicDecisions(t *testing.T) {
	sink, collector := startPipeline(t, headSamplerPipeline)

	topo := loadTopology(t, passthroughTopology)
	const genSeed, idSeed = 7, 99

	first := generateAndCapture(t, topo, collector.OTLPEndpoint, 30, genSeed, newSeededIDGenerator(idSeed))
	firstKept := receivedKeys(sink.WaitSettled(samplerSettleIdle, settleMax))

	sink.Reset()
	second := generateAndCapture(t, topo, collector.OTLPEndpoint, 30, genSeed, newSeededIDGenerator(idSeed))
	secondKept := receivedKeys(sink.WaitSettled(samplerSettleIdle, settleMax))

	// Guard the premise: both runs must have replayed the same spans.
	assertSameSpans(t, sentKeys(first), sentKeys(second))

	if len(firstKept) == 0 || len(firstKept) >= len(first) {
		t.Fatalf("expected partial sampling: sent %d, kept %d", len(first), len(firstKept))
	}
	if len(firstKept) != len(secondKept) {
		t.Fatalf("keep set size changed between identical runs: %d then %d", len(firstKept), len(secondKept))
	}
	for key := range firstKept {
		if _, ok := secondKept[key]; !ok {
			t.Fatalf("span %s kept in first run but dropped in second", key)
		}
	}
}

// TestSampling_TailWholeTraces asserts trace completeness where it actually
// bites: a tail sampler buffers spans and decides per trace, so a bug in its
// buffering (evicting part of a trace, deciding before all spans arrive)
// shows up as a partially kept trace or an orphaned span.
func TestSampling_TailWholeTraces(t *testing.T) {
	if _, ok := pipelinetest.CollectorBinary(); !ok {
		t.Skipf("no collector binary (set %s or install otelcol)", pipelinetest.BinaryEnv)
	}
	if !pipelinetest.SupportsComponent("tail_sampling") {
		t.Skip("collector build lacks the tail_sampling processor (use otelcol-contrib)")
	}
	sink, collector := startPipeline(t, tailSamplingPipeline)

	topo := loadTopology(t, passthroughTopology)
	sent := generateAndCapture(t, topo, collector.OTLPEndpoint, 40, 7, nil)
	spans := sink.WaitSettled(tailSettleIdle, settleMax)
	received := receivedKeys(spans)

	if len(received) == 0 || len(received) >= len(sent) {
		t.Fatalf("expected partial sampling: sent %d, received %d", len(sent), len(received))
	}
	assertNoFabrication(t, sentKeys(sent), received)
	assertParentsKept(t, spans)
	assertWholeTraces(t, sent, received)
}

// recordingT captures Fatalf calls so the invariant helpers can be tested
// against data that must fail them. Unlike testing.T, it does not stop the
// caller, so helpers may record several failures; only the first matters.
type recordingT struct {
	failure string
}

func (r *recordingT) Helper() {}

func (r *recordingT) Fatalf(format string, args ...any) {
	if r.failure == "" {
		r.failure = fmt.Sprintf(format, args...)
	}
}

// TestSamplingInvariantHelpers feeds hand-crafted violations to each invariant
// check and confirms it trips, then confirms clean data passes. A correct
// collector never exercises the failure paths, so this is the only always-on
// coverage they get; it needs no collector binary.
func TestSamplingInvariantHelpers(t *testing.T) {
	traceID := bytes16(1)
	root := pbSpan(traceID, 1, 0)
	child := pbSpan(traceID, 2, 1)
	grandchild := pbSpan(traceID, 3, 2)

	t.Run("orphan detected", func(t *testing.T) {
		rec := &recordingT{}
		assertParentsKept(rec, []*tracepb.Span{root, grandchild})
		if rec.failure == "" {
			t.Fatal("assertParentsKept passed a span whose parent was dropped")
		}
	})

	t.Run("complete lineage passes", func(t *testing.T) {
		rec := &recordingT{}
		assertParentsKept(rec, []*tracepb.Span{root, child, grandchild})
		if rec.failure != "" {
			t.Fatalf("assertParentsKept failed complete lineage: %s", rec.failure)
		}
	})

	sentStubs := []tracetest.SpanStub{stub(traceID, 1), stub(traceID, 2)}

	t.Run("partial trace detected", func(t *testing.T) {
		rec := &recordingT{}
		assertWholeTraces(rec, sentStubs, receivedKeys([]*tracepb.Span{root}))
		if rec.failure == "" {
			t.Fatal("assertWholeTraces passed a partially kept trace")
		}
	})

	t.Run("whole and dropped traces pass", func(t *testing.T) {
		rec := &recordingT{}
		assertWholeTraces(rec, sentStubs, receivedKeys([]*tracepb.Span{root, child}))
		assertWholeTraces(rec, sentStubs, receivedKeys(nil))
		if rec.failure != "" {
			t.Fatalf("assertWholeTraces failed valid sampling: %s", rec.failure)
		}
	})

	t.Run("fabricated span detected", func(t *testing.T) {
		rec := &recordingT{}
		assertNoFabrication(rec, receivedKeys([]*tracepb.Span{root}), receivedKeys([]*tracepb.Span{root, child}))
		if rec.failure == "" {
			t.Fatal("assertNoFabrication passed a span that was never sent")
		}
	})
}

// bytes16 builds a trace ID whose last byte is b.
func bytes16(b byte) [16]byte {
	var id [16]byte
	id[15] = b
	return id
}

// pbSpan builds an OTLP span with small-integer span and parent IDs; a parent
// of 0 means a root span.
func pbSpan(traceID [16]byte, spanID, parentID byte) *tracepb.Span {
	s := &tracepb.Span{
		TraceId: traceID[:],
		SpanId:  []byte{0, 0, 0, 0, 0, 0, 0, spanID},
	}
	if parentID != 0 {
		s.ParentSpanId = []byte{0, 0, 0, 0, 0, 0, 0, parentID}
	}
	return s
}

// stub builds a sent-span stub matching pbSpan's identity scheme.
func stub(traceID [16]byte, spanID byte) tracetest.SpanStub {
	return tracetest.SpanStub{
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID,
			SpanID:  [8]byte{0, 0, 0, 0, 0, 0, 0, spanID},
		}),
	}
}

// assertNoFabrication checks every received span was actually sent: a
// sampler may only drop spans, never invent or duplicate identities.
func assertNoFabrication(t testingT, sent, received map[string]struct{}) {
	t.Helper()

	for key := range received {
		if _, ok := sent[key]; !ok {
			t.Fatalf("received span %s that was never sent", key)
		}
	}
}

// assertParentsKept checks parent-child preservation: every received span
// with a parent has that parent in the received set too. A root span has a
// zero parent ID and is exempt.
func assertParentsKept(t testingT, received []*tracepb.Span) {
	t.Helper()

	keys := receivedKeys(received)
	for _, s := range received {
		parent := s.GetParentSpanId()
		if !validID(parent) {
			continue
		}
		parentKey := spanKey(s.GetTraceId(), parent)
		if _, ok := keys[parentKey]; !ok {
			t.Fatalf("orphaned span: %s was kept but its parent %s was dropped",
				spanKey(s.GetTraceId(), s.GetSpanId()), parentKey)
		}
	}
}

// assertWholeTraces checks trace completeness: for every trace with at least
// one received span, every span sent for that trace was received.
func assertWholeTraces(t testingT, sent []tracetest.SpanStub, received map[string]struct{}) {
	t.Helper()

	sentByTrace := make(map[string][]string)
	for _, s := range sent {
		tid := s.SpanContext.TraceID()
		sid := s.SpanContext.SpanID()
		key := spanKey(tid[:], sid[:])
		sentByTrace[hex.EncodeToString(tid[:])] = append(sentByTrace[hex.EncodeToString(tid[:])], key)
	}

	keptTraces := make(map[string]struct{})
	for key := range received {
		tid, _, _ := strings.Cut(key, ":")
		keptTraces[tid] = struct{}{}
	}

	for tid := range keptTraces {
		for _, key := range sentByTrace[tid] {
			if _, ok := received[key]; !ok {
				t.Fatalf("partially sampled trace %s: span %s was dropped while other spans of the trace were kept", tid, key)
			}
		}
	}
}

// validID reports whether an ID is present and non-zero. OTLP encodes a
// missing parent as an empty or all-zero span ID.
func validID(id []byte) bool {
	for _, b := range id {
		if b != 0 {
			return true
		}
	}
	return false
}

// seededIDGenerator is a deterministic replacement for the SDK's random
// trace/span ID generator. Two generators built from the same seed hand out
// the same ID sequence, so two generation runs with the same topology seed
// produce byte-identical span identities — the precondition for testing
// sampling consistency.
type seededIDGenerator struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func newSeededIDGenerator(seed uint64) sdktrace.IDGenerator {
	return &seededIDGenerator{rng: rand.New(rand.NewPCG(seed, 0))} //nolint:gosec // not security-sensitive
}

func (g *seededIDGenerator) NewIDs(_ context.Context) (trace.TraceID, trace.SpanID) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var tid trace.TraceID
	for !tid.IsValid() {
		binary.BigEndian.PutUint64(tid[:8], g.rng.Uint64())
		binary.BigEndian.PutUint64(tid[8:], g.rng.Uint64())
	}
	return tid, g.newSpanIDLocked()
}

func (g *seededIDGenerator) NewSpanID(_ context.Context, _ trace.TraceID) trace.SpanID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.newSpanIDLocked()
}

func (g *seededIDGenerator) newSpanIDLocked() trace.SpanID {
	var sid trace.SpanID
	for !sid.IsValid() {
		binary.BigEndian.PutUint64(sid[:], g.rng.Uint64())
	}
	return sid
}
