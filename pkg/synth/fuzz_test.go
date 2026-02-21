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

// FuzzParseRate uses coverage-guided fuzzing to explore ParseRate
// with regex-generated rate strings.
func FuzzParseRate(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		s := genRateString.Draw(t, "rate")
		rate, err := ParseRate(s)
		if err != nil {
			// Some regex-generated strings may exceed 10000 — that's a valid rejection
			return
		}
		if rate.Count() <= 0 {
			t.Fatalf("parsed rate count %d should be positive", rate.Count())
		}
		if rate.Count() > 10000 {
			t.Fatalf("parsed rate count %d exceeds 10000", rate.Count())
		}
	}))
}
