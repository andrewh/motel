// Fuzz targets wrapping property tests via rapid.MakeFuzz
// Run with: go test -fuzz=FuzzValidateConfig ./pkg/synth/ -fuzztime=30s
package synth

import (
	"testing"

	"pgregory.net/rapid"
)

// FuzzValidateConfig uses coverage-guided fuzzing to explore ValidateConfig
// with randomly generated valid configs. Any config produced by genSimpleConfig
// must be accepted by ValidateConfig.
func FuzzValidateConfig(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		if err := ValidateConfig(cfg); err != nil {
			t.Fatalf("ValidateConfig rejected valid config: %v", err)
		}
	}))
}

// FuzzBuildTopology uses coverage-guided fuzzing to explore BuildTopology
// with randomly generated valid configs. Verifies that all Ref fields are
// set correctly and no cycles exist.
func FuzzBuildTopology(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		for svcName, svc := range topo.Services {
			for opName, op := range svc.Operations {
				expectedRef := svcName + "." + opName
				if op.Ref != expectedRef {
					t.Fatalf("op.Ref %q != expected %q", op.Ref, expectedRef)
				}
			}
		}
	}))
}

// FuzzParseDistribution uses coverage-guided fuzzing to explore ParseDistribution
// round-trip: generate a distribution, serialise it, parse it back, and check equality.
func FuzzParseDistribution(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		dur := genDurationString.Draw(t, "dur")
		dist, err := ParseDistribution(dur)
		if err != nil {
			t.Fatalf("ParseDistribution(%q): %v", dur, err)
		}
		s := dist.String()
		parsed, err := ParseDistribution(s)
		if err != nil {
			t.Fatalf("ParseDistribution round-trip(%q): %v", s, err)
		}
		if parsed.Mean != dist.Mean {
			t.Fatalf("mean mismatch: %v != %v", parsed.Mean, dist.Mean)
		}
	}))
}

// FuzzCheckMaxDepthBounds uses coverage-guided fuzzing to verify that
// static MaxDepth always bounds sampled observations.
func FuzzCheckMaxDepthBounds(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		cfg := genCheckConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}
		staticDepth, _ := MaxDepth(topo)
		sampled := SampleTraces(topo, 100, rapid.Uint64().Draw(t, "seed"), 0)
		if sampled.MaxDepth > staticDepth {
			t.Fatalf("sampled depth %d exceeds static bound %d", sampled.MaxDepth, staticDepth)
		}
	}))
}

// FuzzCheckMaxSpansBounds uses coverage-guided fuzzing to verify that
// static MaxSpans always bounds sampled observations.
func FuzzCheckMaxSpansBounds(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		cfg := genCheckConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}
		staticSpans, _ := MaxSpans(topo)
		sampled := SampleTraces(topo, 100, rapid.Uint64().Draw(t, "seed"), 0)
		if sampled.MaxSpans > staticSpans {
			t.Fatalf("sampled spans %d exceeds static bound %d", sampled.MaxSpans, staticSpans)
		}
	}))
}

// FuzzCheckMaxFanOutBounds uses coverage-guided fuzzing to verify that
// static MaxFanOut always bounds sampled observations.
func FuzzCheckMaxFanOutBounds(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		cfg := genCheckConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}
		staticFanOut, _ := MaxFanOut(topo)
		sampled := SampleTraces(topo, 100, rapid.Uint64().Draw(t, "seed"), 0)
		if sampled.MaxFanOut > staticFanOut {
			t.Fatalf("sampled fan-out %d exceeds static bound %d", sampled.MaxFanOut, staticFanOut)
		}
	}))
}

// FuzzDistributionOrdering uses coverage-guided fuzzing to verify that
// percentile distributions are monotonic (p50 <= p95 <= p99 <= max) and
// that max equals the running maximum from SampleTraces.
func FuzzDistributionOrdering(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		cfg := genCheckConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		sampled := SampleTraces(topo, 100, rapid.Uint64().Draw(t, "seed"), 0)
		depthDist, spansDist, fanOutDist := sampled.Distribution.Summary()

		for _, tc := range []struct {
			name string
			dist DistributionSummary
			max  int
		}{
			{"depth", depthDist, sampled.MaxDepth},
			{"spans", spansDist, sampled.MaxSpans},
			{"fan-out", fanOutDist, sampled.MaxFanOut},
		} {
			if tc.dist.P50 > tc.dist.P95 {
				t.Fatalf("%s: p50 (%d) > p95 (%d)", tc.name, tc.dist.P50, tc.dist.P95)
			}
			if tc.dist.P95 > tc.dist.P99 {
				t.Fatalf("%s: p95 (%d) > p99 (%d)", tc.name, tc.dist.P95, tc.dist.P99)
			}
			if tc.dist.P99 > tc.dist.Max {
				t.Fatalf("%s: p99 (%d) > max (%d)", tc.name, tc.dist.P99, tc.dist.Max)
			}
			if tc.dist.Max != tc.max {
				t.Fatalf("%s: distribution max (%d) != MaxX (%d)", tc.name, tc.dist.Max, tc.max)
			}
		}
	}))
}

// FuzzParseRate uses coverage-guided fuzzing to explore ParseRate
// with regex-generated rate strings.
func FuzzParseRate(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		s := genRateString.Draw(t, "rate")
		rate, err := ParseRate(s)
		if err != nil {
			// Some regex-generated strings may exceed MaxRateCount — that's a valid rejection
			return
		}
		if rate.Count() <= 0 {
			t.Fatalf("parsed rate count %d should be positive", rate.Count())
		}
		if rate.Count() > MaxRateCount {
			t.Fatalf("parsed rate count %d exceeds %d", rate.Count(), MaxRateCount)
		}
	}))
}

// FuzzParseErrorRate explores parseErrorRate with both percentage and bare
// float formats, including edge cases like negative values and out-of-range.
func FuzzParseErrorRate(f *testing.F) {
	f.Add([]byte{0}) // seed
	f.Fuzz(func(t *testing.T, data []byte) {
		s := string(data)
		v, err := parseErrorRate(s)
		if err != nil {
			return
		}
		if v < 0 || v > 1 {
			t.Fatalf("parseErrorRate(%q) = %f, want [0, 1]", s, v)
		}
	})
}

// FuzzValidateCallConfig explores validateCallConfig with generated call
// configs covering timeout, retry_backoff, negative count/retries, and
// invalid conditions.
func FuzzValidateCallConfig(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		target := rapid.SampledFrom([]string{
			"svc.op",
			"missing",
			"bad.target",
			"a.b",
		}).Draw(t, "target")

		knownOps := map[string]bool{"svc.op": true, "a.b": true}

		call := CallConfig{
			Target: target,
		}
		if rapid.Bool().Draw(t, "hasCount") {
			call.Count = rapid.IntRange(-1, 5).Draw(t, "count")
		}
		if rapid.Bool().Draw(t, "hasRetries") {
			call.Retries = rapid.IntRange(-1, 3).Draw(t, "retries")
		}
		if rapid.Bool().Draw(t, "hasProb") {
			call.Probability = rapid.Float64Range(-0.5, 1.5).Draw(t, "prob")
		}
		if rapid.Bool().Draw(t, "hasCond") {
			call.Condition = rapid.SampledFrom([]string{
				"", "on-error", "on-success", "invalid",
			}).Draw(t, "cond")
		}
		if rapid.Bool().Draw(t, "hasTimeout") {
			call.Timeout = rapid.SampledFrom([]string{
				"", "100ms", "0s", "-1s", "bad",
			}).Draw(t, "timeout")
		}
		if rapid.Bool().Draw(t, "hasBackoff") {
			call.RetryBackoff = rapid.SampledFrom([]string{
				"", "10ms", "-1s", "bad",
			}).Draw(t, "backoff")
		}
		if rapid.Bool().Draw(t, "hasAsync") {
			call.Async = true
		}

		// We don't care whether it passes or fails — just that it doesn't panic
		_ = validateCallConfig(call, knownOps)
	}))
}
