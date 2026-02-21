// Fuzz targets wrapping property tests via rapid.MakeFuzz
// Run with: go test -fuzz=FuzzMarshalRoundTrip ./pkg/synth/traceimport/ -fuzztime=30s
package traceimport

import (
	"os"
	"testing"

	"github.com/andrewh/motel/pkg/synth"
	"pgregory.net/rapid"
)

// FuzzMarshalRoundTrip uses coverage-guided fuzzing to explore the full
// import pipeline: generate spans → build trees → collect stats → marshal YAML
// → load config → validate. Any generated input must produce valid output.
func FuzzMarshalRoundTrip(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(func(t *rapid.T) {
		spans := genMultiTraceSpans(t)
		trees := BuildTrees(spans, nil)

		collector := NewStatsCollector()
		collector.CollectFromTrees(trees)

		serviceAttrs := inferServiceAttributes(spans)
		windowSecs := computeWindow(trees)

		yamlBytes, err := MarshalConfig(collector, serviceAttrs, len(trees), len(spans), windowSecs)
		if err != nil {
			t.Fatalf("MarshalConfig: %v", err)
		}

		tmpFile, err := os.CreateTemp("", "fuzz-test-*.yaml")
		if err != nil {
			t.Fatalf("creating temp file: %v", err)
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.Write(yamlBytes); err != nil {
			tmpFile.Close()
			t.Fatalf("writing temp file: %v", err)
		}
		tmpFile.Close()

		cfg, err := synth.LoadConfig(tmpFile.Name())
		if err != nil {
			t.Fatalf("LoadConfig failed on generated YAML:\n%s\nerror: %v", yamlBytes, err)
		}
		if err := synth.ValidateConfig(cfg); err != nil {
			t.Fatalf("ValidateConfig failed on generated YAML:\n%s\nerror: %v", yamlBytes, err)
		}
	}))
}
