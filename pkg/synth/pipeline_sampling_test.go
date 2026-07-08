package synth

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/pipelinetest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"pgregory.net/rapid"
)

// Sampling trace integrity invariants (issue 74). Sampling processors decide
// which traces survive a pipeline; these tests assert the decisions never
// break trace structure, using the checks in pkg/pipelinetest:
//
//   - Parent-child preservation: if a child span is kept, its parent is kept
//     (no orphaned spans) — CheckParentsKept.
//   - Trace completeness: if any span from a trace is kept, all spans from
//     that trace are kept — CheckWholeTraces.
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

	// tailSettleIdle must exceed the tail sampler's total release latency:
	// decision_wait (1s) plus the processor's internal policy-evaluation tick
	// (~1s), with margin for subprocess scheduling jitter under load. A
	// shorter window would mistake the silence while traces sit in the
	// decision buffer for a drained pipeline.
	tailSettleIdle = 4 * time.Second

	settleMax = 30 * time.Second
)

// headSamplerPipeline samples 50% of traces at the head. hash_seed mode makes
// the keep/drop decision a pure function of the trace ID, so it is
// deterministic across runs and identical for every span of a trace.
var headSamplerPipeline = pipelinetest.TracesConfig(`  probabilistic_sampler:
    sampling_percentage: 50
    mode: hash_seed
    hash_seed: 22
`, "probabilistic_sampler")

// tailSamplingPipeline buffers whole traces for decision_wait, then keeps 50%
// of them. The tail_sampling processor ships in otelcol-contrib, not the
// reference otelcol build, so the test using it checks SupportsComponent.
var tailSamplingPipeline = pipelinetest.TracesConfig(`  tail_sampling:
    decision_wait: 1s
    policies:
      - name: keep-half
        type: probabilistic
        probabilistic:
          sampling_percentage: 50
`, "tail_sampling")

// TestSampling_TraceIntegrity drives a fixed multi-service topology through a
// 50% head sampler and asserts all structural invariants at once: the
// survivors are a subset of what was sent, no kept span is orphaned, and
// every kept trace is complete.
func TestSampling_TraceIntegrity(t *testing.T) {
	sink, collector := startPipeline(t, headSamplerPipeline)

	topo := loadTopology(t, passthroughTopology)
	sent := generateAndSend(t, topo, collector.OTLPEndpoint, 40, 7)
	spans := sink.WaitSettled(samplerSettleIdle, settleMax)

	assertPartialIntegrity(t, sent, spans)
}

// TestSampling_TraceIntegrityProperty asserts the same invariants for any
// generated topology: whatever the trace shape — depth, fan-out, error mix —
// the sampler must keep or drop traces whole. One collector is reused across
// all draws, with sent identities accumulating rather than resetting the sink
// between draws (see TestPipeline_AllSpansRoundTripProperty for why a reset
// races an in-flight export).
func TestSampling_TraceIntegrityProperty(t *testing.T) {
	sink, collector := startPipeline(t, headSamplerPipeline)
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
		spans := sink.WaitSettled(propertySettleIdle, settleMax)

		if err := pipelinetest.CheckNoFabrication(sent, spans); err != nil {
			t.Fatal(err)
		}
		if err := pipelinetest.CheckParentsKept(spans); err != nil {
			t.Fatal(err)
		}
		if err := pipelinetest.CheckWholeTraces(sent, spans); err != nil {
			t.Fatal(err)
		}
	})
}

// TestSampling_DeterministicDecisions replays the identical span set through
// the same sampler twice and asserts the keep/drop decisions match. A seeded
// ID generator makes both runs use the same trace and span IDs, which is what
// a hash_seed sampler decides on. Each run gets its own collector and sink, so
// a late export from the first run cannot bleed into the second.
func TestSampling_DeterministicDecisions(t *testing.T) {
	const genSeed, idSeed = 7, 99

	firstSent, firstKept := sampleWithSeed(t, genSeed, idSeed)
	secondSent, secondKept := sampleWithSeed(t, genSeed, idSeed)

	if !equalKeys(firstSent, secondSent) {
		t.Fatal("premise broken: identical seeds replayed different span identities")
	}
	if len(firstKept) == 0 || len(firstKept) >= len(firstSent) {
		t.Fatalf("expected partial sampling: sent %d, kept %d", len(firstSent), len(firstKept))
	}
	if !equalKeys(firstKept, secondKept) {
		t.Fatal("keep/drop decision differed between identical runs")
	}
}

// TestSampling_TailWholeTraces asserts trace completeness where it actually
// bites: a tail sampler buffers spans and decides per trace, so a bug in its
// buffering (evicting part of a trace, deciding before all spans arrive)
// shows up as a partially kept trace or an orphaned span.
func TestSampling_TailWholeTraces(t *testing.T) {
	if !pipelinetest.SupportsComponent("tail_sampling") {
		t.Skip("no collector with the tail_sampling processor (set MOTEL_COLLECTOR_BIN to an otelcol-contrib build)")
	}
	sink, collector := startPipeline(t, tailSamplingPipeline)

	topo := loadTopology(t, passthroughTopology)
	sent := generateAndSend(t, topo, collector.OTLPEndpoint, 40, 7)
	spans := sink.WaitSettled(tailSettleIdle, settleMax)

	assertPartialIntegrity(t, sent, spans)
}

// assertPartialIntegrity checks that a sampler kept some but not all of sent,
// and that the survivors satisfy every trace-integrity invariant.
func assertPartialIntegrity(t *testing.T, sent *pipelinetest.Sent, spans []*tracepb.Span) {
	t.Helper()

	received := pipelinetest.ReceivedKeys(spans)
	if len(received) == 0 || len(received) >= sent.Len() {
		t.Fatalf("expected partial sampling: sent %d, received %d", sent.Len(), len(received))
	}
	if err := pipelinetest.CheckNoFabrication(sent, spans); err != nil {
		t.Fatal(err)
	}
	if err := pipelinetest.CheckParentsKept(spans); err != nil {
		t.Fatal(err)
	}
	if err := pipelinetest.CheckWholeTraces(sent, spans); err != nil {
		t.Fatal(err)
	}
}

// sampleWithSeed runs one head-sampling pipeline end to end with the given
// generation and ID seeds, returning the sent span identities and the kept
// (received) identities. Each call starts its own collector and sink.
func sampleWithSeed(t *testing.T, genSeed, idSeed uint64) (sent, kept map[string]struct{}) {
	t.Helper()

	sink, collector := startPipeline(t, headSamplerPipeline)
	topo := loadTopology(t, passthroughTopology)

	stubs := generateAndCapture(t, topo, collector.OTLPEndpoint, 30, genSeed, newSeededIDGenerator(idSeed))
	sent = make(map[string]struct{}, len(stubs))
	for _, s := range stubs {
		tid := s.SpanContext.TraceID()
		sid := s.SpanContext.SpanID()
		sent[pipelinetest.SpanKey(tid[:], sid[:])] = struct{}{}
	}
	kept = pipelinetest.ReceivedKeys(sink.WaitSettled(samplerSettleIdle, settleMax))
	return sent, kept
}

// equalKeys reports whether two identity sets contain exactly the same keys.
func equalKeys(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
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
	return randomTraceID(g.rng.Uint64), randomSpanID(g.rng.Uint64)
}

func (g *seededIDGenerator) NewSpanID(_ context.Context, _ trace.TraceID) trace.SpanID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return randomSpanID(g.rng.Uint64)
}
