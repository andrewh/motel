// Fuzz targets wrapping property tests via rapid.MakeFuzz
// Run with: go test -fuzz=FuzzMarshalRoundTrip ./pkg/synth/traceimport/ -fuzztime=30s
package traceimport

import (
	"bytes"
	"os"
	"testing"

	"github.com/andrewh/motel/pkg/synth"
	"pgregory.net/rapid"
)

// FuzzParseSpans feeds arbitrary bytes to ParseSpans with each format,
// exercising format detection, JSON parsing, error paths, and attribute
// extraction. The property is that ParseSpans must not panic.
func FuzzParseSpans(f *testing.F) {
	// Seed with valid inputs for each format
	f.Add([]byte(`{"Name":"op","SpanContext":{"TraceID":"aaa","SpanID":"bbb"},"Parent":{"TraceID":"aaa","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`))
	f.Add([]byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"scope":{"name":"api"},"spans":[{"traceId":"AQIDBAUGBwgJCgsMDQ4PEA==","spanId":"AQIDBAUGBwg=","name":"op","startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000000030000000","status":{},"attributes":[{"key":"http.method","value":{"stringValue":"GET"}},{"key":"count","value":{"intValue":"42"}},{"key":"ok","value":{"boolValue":true}}]}]}]}]}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(`{"something":"else"}`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		// Test auto-detection
		_, _ = ParseSpans(bytes.NewReader(data), FormatAuto)
		// Test explicit formats
		_, _ = ParseSpans(bytes.NewReader(data), FormatStdouttrace)
		_, _ = ParseSpans(bytes.NewReader(data), FormatOTLP)
	})
}

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
