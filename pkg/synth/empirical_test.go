package synth

import (
	"math"
	"testing"
)

// Validation of Check against published empirical trace studies.
//
// The topologies under docs/examples model structural characteristics
// reported from production trace analyses:
//
//   - Luo et al., "An In-Depth Study of Microservice Call Graph and Runtime
//     Performance" (IEEE TPDS 2022): Alibaba call graphs with depth 2-6 for
//     top services, children set sizes 1-10, and a 16.2% repeated call rate.
//   - Huye, Shkuro, and Sambasivan, "Lifting the Veil on Meta's Microservice
//     Architecture" (USENIX ATC 2023) and Du et al., "A Microservice Graph
//     Generator with Production Characteristics" (ICS 2025): wide, shallow
//     Meta workflows with children set sizes up to 50.
//
// These tests verify that the static analysis reproduces the structural
// bounds the topologies were built to exhibit, and that Monte Carlo sampling
// stays within both the static bounds and the published ranges. See
// docs/research/empirical-validation.md for the full comparison.

const (
	alibabaTopologyPath = "../../docs/examples/alibaba-call-graph.yaml"
	metaTopologyPath    = "../../docs/examples/meta-wide-fanout.yaml"

	empiricalSamples = 1000
	empiricalSeed    = 42
)

// Published Alibaba characteristics (Luo et al. TPDS 2022) and the values
// the modelled topology is constructed to exhibit.
const (
	alibabaDepth     = 6
	alibabaMinDepth  = 2
	alibabaFanOut    = 10
	alibabaWorstCase = 24

	// Deterministic calls alone give orchestrator.compose five children
	// (product, customer, and three memcached calls), so sampled fan-out
	// can never drop below this.
	alibabaMinFanOut = 5

	publishedRepeatedCallRate = 0.162
	repeatedCallRateTolerance = 0.01
)

// Published Meta characteristics (Huye et al. ATC 2023, Du et al. ICS 2025)
// and the values the modelled topology is constructed to exhibit. The
// topology is fully deterministic, so sampled values must match exactly.
const (
	metaDepth     = 3
	metaFanOut    = 50
	metaWorstCase = 54
)

func loadEmpiricalTopology(t *testing.T, path string) (*Config, *Topology) {
	t.Helper()
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", path, err)
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig(%s): %v", path, err)
	}
	topo, err := BuildTopology(cfg)
	if err != nil {
		t.Fatalf("BuildTopology(%s): %v", path, err)
	}
	return cfg, topo
}

// repeatedCallRate returns the fraction of call edges with count > 1,
// matching Luo et al.'s definition of repeated calls: the same caller
// invoking the same callee more than once within a call graph.
func repeatedCallRate(cfg *Config) float64 {
	total, repeated := 0, 0
	for _, svc := range cfg.Services {
		for _, op := range svc.Operations {
			for _, call := range op.Calls {
				total++
				if call.Count > 1 {
					repeated++
				}
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(repeated) / float64(total)
}

func TestEmpiricalAlibabaStaticBounds(t *testing.T) {
	_, topo := loadEmpiricalTopology(t, alibabaTopologyPath)

	depth, path := MaxDepth(topo)
	if depth != alibabaDepth {
		t.Errorf("static depth: expected %d, got %d (path: %v)", alibabaDepth, depth, path)
	}
	if len(path) != alibabaDepth+1 {
		t.Errorf("depth path: expected %d operations, got %d: %v", alibabaDepth+1, len(path), path)
	}

	fanOut, ref := MaxFanOut(topo)
	if fanOut != alibabaFanOut {
		t.Errorf("static fan-out: expected %d, got %d", alibabaFanOut, fanOut)
	}
	if ref != "orchestrator.compose" {
		t.Errorf("worst fan-out ref: expected orchestrator.compose, got %s", ref)
	}

	spans, root := MaxSpans(topo)
	if spans != alibabaWorstCase {
		t.Errorf("static spans: expected %d, got %d", alibabaWorstCase, spans)
	}
	if root != "gateway.POST /checkout" {
		t.Errorf("worst root: expected gateway.POST /checkout, got %s", root)
	}
}

func TestEmpiricalAlibabaRepeatedCallRate(t *testing.T) {
	cfg, _ := loadEmpiricalTopology(t, alibabaTopologyPath)

	rate := repeatedCallRate(cfg)
	if math.Abs(rate-publishedRepeatedCallRate) > repeatedCallRateTolerance {
		t.Errorf("repeated call rate: expected %.3f +/- %.3f (Luo et al. TPDS 2022), got %.3f",
			publishedRepeatedCallRate, repeatedCallRateTolerance, rate)
	}
}

func TestEmpiricalAlibabaSampledDistribution(t *testing.T) {
	_, topo := loadEmpiricalTopology(t, alibabaTopologyPath)

	sampled := SampleTraces(topo, empiricalSamples, empiricalSeed, 0)
	if sampled.TracesRun != empiricalSamples {
		t.Fatalf("expected %d traces, got %d", empiricalSamples, sampled.TracesRun)
	}

	if sampled.MaxDepth != alibabaDepth {
		t.Errorf("sampled max depth: expected %d, got %d", alibabaDepth, sampled.MaxDepth)
	}
	if sampled.MaxFanOut < alibabaMinFanOut || sampled.MaxFanOut > alibabaFanOut {
		t.Errorf("sampled max fan-out: expected within [%d, %d], got %d",
			alibabaMinFanOut, alibabaFanOut, sampled.MaxFanOut)
	}
	if sampled.MaxSpans > alibabaWorstCase {
		t.Errorf("sampled max spans %d exceeds static worst case %d", sampled.MaxSpans, alibabaWorstCase)
	}

	depthDist, _, fanOutDist := sampled.Distribution.Summary()
	if depthDist.P50 < alibabaMinDepth || depthDist.P50 > alibabaDepth {
		t.Errorf("depth p50: expected within published range [%d, %d], got %d",
			alibabaMinDepth, alibabaDepth, depthDist.P50)
	}
	if depthDist.P99 > alibabaDepth {
		t.Errorf("depth p99: expected at most %d, got %d", alibabaDepth, depthDist.P99)
	}
	if fanOutDist.P99 > alibabaFanOut {
		t.Errorf("fan-out p99: expected at most %d, got %d", alibabaFanOut, fanOutDist.P99)
	}
}

func TestEmpiricalMetaStaticBounds(t *testing.T) {
	_, topo := loadEmpiricalTopology(t, metaTopologyPath)

	depth, path := MaxDepth(topo)
	if depth != metaDepth {
		t.Errorf("static depth: expected %d, got %d (path: %v)", metaDepth, depth, path)
	}

	fanOut, ref := MaxFanOut(topo)
	if fanOut != metaFanOut {
		t.Errorf("static fan-out: expected %d, got %d", metaFanOut, fanOut)
	}
	if ref != "feed-agg.rank" {
		t.Errorf("worst fan-out ref: expected feed-agg.rank, got %s", ref)
	}

	spans, root := MaxSpans(topo)
	if spans != metaWorstCase {
		t.Errorf("static spans: expected %d, got %d", metaWorstCase, spans)
	}
	if root != "web.GET /feed" {
		t.Errorf("worst root: expected web.GET /feed, got %s", root)
	}
}

func TestEmpiricalMetaSampledMatchesStatic(t *testing.T) {
	_, topo := loadEmpiricalTopology(t, metaTopologyPath)

	sampled := SampleTraces(topo, empiricalSamples, empiricalSeed, 0)
	if sampled.TracesRun != empiricalSamples {
		t.Fatalf("expected %d traces, got %d", empiricalSamples, sampled.TracesRun)
	}

	// The topology has no probabilistic or conditional calls, so every
	// sampled trace must realise the static worst case exactly.
	if sampled.MaxDepth != metaDepth {
		t.Errorf("sampled max depth: expected %d, got %d", metaDepth, sampled.MaxDepth)
	}
	if sampled.MaxFanOut != metaFanOut {
		t.Errorf("sampled max fan-out: expected %d, got %d", metaFanOut, sampled.MaxFanOut)
	}
	if sampled.MaxSpans != metaWorstCase {
		t.Errorf("sampled max spans: expected %d, got %d", metaWorstCase, sampled.MaxSpans)
	}

	depthDist, spansDist, fanOutDist := sampled.Distribution.Summary()
	if depthDist.P50 != metaDepth || depthDist.Max != metaDepth {
		t.Errorf("depth distribution: expected constant %d, got p50=%d max=%d",
			metaDepth, depthDist.P50, depthDist.Max)
	}
	if spansDist.P50 != metaWorstCase || spansDist.Max != metaWorstCase {
		t.Errorf("spans distribution: expected constant %d, got p50=%d max=%d",
			metaWorstCase, spansDist.P50, spansDist.Max)
	}
	if fanOutDist.P50 != metaFanOut || fanOutDist.Max != metaFanOut {
		t.Errorf("fan-out distribution: expected constant %d, got p50=%d max=%d",
			metaFanOut, fanOutDist.P50, fanOutDist.Max)
	}
}
