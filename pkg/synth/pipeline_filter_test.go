package synth

import (
	"testing"

	"github.com/andrewh/motel/pkg/pipelinetest"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"pgregory.net/rapid"
)

// Filtering invariants (issue 75). A filter processor decides span by span
// whether telemetry survives the pipeline; these tests assert its decisions
// are exactly the configured predicate — no span it should drop leaks
// through, no span it should keep disappears — using
// pipelinetest.CheckFilterCorrectness. The test computes the expected
// keep/drop partition client-side by applying the same predicate to the spans
// it captured on the way out, so the check is a pure set comparison.
//
// Like the other pipeline tests, they drive a real collector subprocess and
// skip when no binary is available. They additionally skip when the binary
// lacks the filter processor (a contrib component absent from minimal builds).

// leafFilterPipeline drops db spans — the leaves of passthroughTopology — so
// removing them leaves the remaining lineage intact.
var leafFilterPipeline = pipelinetest.TracesConfig(`  filter:
    error_mode: ignore
    traces:
      span:
        - instrumentation_scope.name == "db"
`, "filter")

// midTreeFilterPipeline drops backend spans, which sit between gateway and db
// in every trace, so every surviving db span is orphaned.
var midTreeFilterPipeline = pipelinetest.TracesConfig(`  filter:
    error_mode: ignore
    traces:
      span:
        - instrumentation_scope.name == "backend"
`, "filter")

// errorFilterPipeline drops error spans regardless of topology shape.
var errorFilterPipeline = pipelinetest.TracesConfig(`  filter:
    error_mode: ignore
    traces:
      span:
        - status.code == STATUS_CODE_ERROR
`, "filter")

// TestFilter_ExactPartition drives a fixed topology through a filter that
// drops db spans and asserts the output is exactly the non-db spans: filter
// correctness with no false positives or negatives.
func TestFilter_ExactPartition(t *testing.T) {
	requireFilter(t)
	sink, collector := startPipeline(t, leafFilterPipeline)

	topo := loadTopology(t, passthroughTopology)
	stubs := generateAndCapture(t, topo, collector.OTLPEndpoint, 20, 3, nil)
	keep, drop := partitionStubs(stubs, func(s tracetest.SpanStub) bool {
		return s.InstrumentationScope.Name == "db"
	})
	if len(drop) == 0 {
		t.Fatal("premise broken: topology generated no db spans")
	}
	spans := sink.WaitSettled(samplerSettleIdle, settleMax)

	if err := pipelinetest.CheckFilterCorrectness(keep, drop, spans); err != nil {
		t.Fatal(err)
	}
}

// TestFilter_ErrorSpansProperty asserts filter correctness for any generated
// topology: whatever the trace shape and error mix, a status-based filter
// keeps exactly the non-error spans. One collector is reused across all
// draws, with the expected partition accumulating rather than resetting the
// sink between draws (see TestPipeline_AllSpansRoundTripProperty for why a
// reset races an in-flight export).
func TestFilter_ErrorSpansProperty(t *testing.T) {
	requireFilter(t)
	sink, collector := startPipeline(t, errorFilterPipeline)

	keep := make(map[string]struct{})
	drop := make(map[string]struct{})

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
		stubs := generateAndCapture(t, topo, collector.OTLPEndpoint, 5, seed, nil)
		if len(stubs) == 0 {
			t.Skip("no spans generated")
		}
		k, d := partitionStubs(stubs, func(s tracetest.SpanStub) bool {
			return s.Status.Code == codes.Error
		})
		merge(keep, k)
		merge(drop, d)
		spans := sink.WaitSettled(propertySettleIdle, settleMax)

		if err := pipelinetest.CheckFilterCorrectness(keep, drop, spans); err != nil {
			t.Fatal(err)
		}
	})
}

// TestFilter_MidTreeDropBreaksLineage demonstrates the interaction bug the
// structural invariants exist to catch: a filter that is perfectly correct
// span by span still breaks traces when its predicate matches mid-tree spans.
// Dropping backend spans orphans every db span, so filter correctness holds
// while parent-child preservation fails — and a tail sampler or trace-level
// tool downstream would see broken lineage.
func TestFilter_MidTreeDropBreaksLineage(t *testing.T) {
	requireFilter(t)
	sink, collector := startPipeline(t, midTreeFilterPipeline)

	topo := loadTopology(t, passthroughTopology)
	stubs := generateAndCapture(t, topo, collector.OTLPEndpoint, 20, 3, nil)
	keep, drop := partitionStubs(stubs, func(s tracetest.SpanStub) bool {
		return s.InstrumentationScope.Name == "backend"
	})
	if len(drop) == 0 {
		t.Fatal("premise broken: topology generated no backend spans")
	}
	spans := sink.WaitSettled(samplerSettleIdle, settleMax)

	if err := pipelinetest.CheckFilterCorrectness(keep, drop, spans); err != nil {
		t.Fatalf("filter applied its predicate incorrectly: %v", err)
	}
	if err := pipelinetest.CheckParentsKept(spans); err == nil {
		t.Fatal("expected orphaned spans after dropping mid-tree spans, but lineage was intact")
	}
}

// requireFilter skips the test when the collector build lacks the filter
// processor.
func requireFilter(t *testing.T) {
	t.Helper()
	if !pipelinetest.SupportsComponent("filter") {
		t.Skip("no collector with the filter processor (set MOTEL_COLLECTOR_BIN to a build that includes it)")
	}
}

// partitionStubs splits captured spans into keep/drop identity sets by the
// drop predicate — the client-side mirror of a filter's configuration.
func partitionStubs(stubs []tracetest.SpanStub, dropPred func(tracetest.SpanStub) bool) (keep, drop map[string]struct{}) {
	keep = make(map[string]struct{})
	drop = make(map[string]struct{})
	for _, s := range stubs {
		tid := s.SpanContext.TraceID()
		sid := s.SpanContext.SpanID()
		key := pipelinetest.SpanKey(tid[:], sid[:])
		if dropPred(s) {
			drop[key] = struct{}{}
		} else {
			keep[key] = struct{}{}
		}
	}
	return keep, drop
}

// merge adds every key of src to dst.
func merge(dst, src map[string]struct{}) {
	for k := range src {
		dst[k] = struct{}{}
	}
}
