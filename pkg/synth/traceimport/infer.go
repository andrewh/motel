// Package traceimport infers a motel topology from recorded trace data.
// The pipeline parses spans, reconstructs trace trees, computes per-operation
// statistics, and serialises the result as a topology YAML file.
package traceimport

import (
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/andrewh/motel/pkg/synth"
)

// Options controls import behaviour.
type Options struct {
	Format           Format
	MinTraces        int
	Warnings         io.Writer // defaults to os.Stderr
	MetaProfile      string
	MetaIncludeEmpty bool
	// RecordTo, when non-nil, receives a newline-delimited replay recording of
	// the source traces alongside the inferred topology. Not supported for
	// Meta summary imports, which carry no per-trace span data.
	RecordTo io.Writer
}

// Result contains the inferred topology and source counts from an import.
//
// For span-based imports (OTLP, stdouttrace, auto) the counts are literal. For
// Meta summary imports they are weighted estimates rather than observed values;
// see the field comments.
type Result struct {
	// YAML is the inferred synth topology.
	YAML []byte
	// TraceCount is the number of source traces. For Meta summary imports it is
	// the total weighted parent-sample count rather than a literal trace count.
	TraceCount int
	// SpanCount is the number of source spans. For Meta summary imports it is an
	// estimate derived from the weighted call counts rather than a literal span
	// count.
	SpanCount int
}

// Import reads trace spans, analyses them, and produces a synth YAML topology.
func Import(r io.Reader, opts Options) (Result, error) {
	if opts.Warnings == nil {
		opts.Warnings = os.Stderr
	}
	if opts.MinTraces == 0 {
		opts.MinTraces = 1
	}
	if opts.Format == FormatMetaSummary {
		if opts.RecordTo != nil {
			return Result{}, fmt.Errorf("--record is not supported for meta-summary input (no per-trace span data)")
		}
		return importMetaSummary(r, opts)
	}

	// Step 1: Parse spans
	spans, err := ParseSpans(r, opts.Format)
	if err != nil {
		return Result{}, err
	}

	// Step 2: Build trace trees
	trees := BuildTrees(spans, opts.Warnings)

	// Optional: write a replay recording sidecar of the source traces.
	if opts.RecordTo != nil {
		if err := WriteRecording(trees, opts.RecordTo); err != nil {
			return Result{}, fmt.Errorf("writing recording: %w", err)
		}
	}

	traceCount := len(trees)
	if traceCount < opts.MinTraces {
		_, _ = fmt.Fprintf(opts.Warnings, "warning: only %d traces available (requested minimum: %d); results may be inaccurate\n",
			traceCount, opts.MinTraces)
	}
	if traceCount == 1 {
		_, _ = fmt.Fprintf(opts.Warnings, "warning: only 1 trace available; duration distributions will be exact values. Use more traces for statistical accuracy.\n")
	}

	// Step 3: Collect statistics
	collector := NewStatsCollector()
	collector.CollectFromTrees(trees)
	reportConfidenceDiagnostics(collector, opts.MinTraces, opts.Warnings)

	// Step 4: Infer service-level constant attributes
	serviceAttrs := inferServiceAttributes(spans)

	// Step 5: Compute traffic rate window
	windowSecs := computeWindow(trees)

	// Step 6: Marshal to YAML
	yamlBytes, err := MarshalConfig(collector, serviceAttrs, traceCount, len(spans), windowSecs)
	if err != nil {
		return Result{}, err
	}

	// Step 7: Round-trip validation
	if err := validateRoundTrip(yamlBytes); err != nil {
		return Result{}, fmt.Errorf("round-trip validation failed (this is a bug): %w", err)
	}

	return Result{
		YAML:       yamlBytes,
		TraceCount: traceCount,
		SpanCount:  len(spans),
	}, nil
}

// inferServiceAttributes finds attributes with the same value on every span of a service.
func inferServiceAttributes(spans []Span) map[string]map[string]string {
	type attrAccum struct {
		value    string
		count    int
		constant bool
	}

	// Per-service: attribute key -> accumulator
	svcAccum := make(map[string]map[string]*attrAccum)
	svcCounts := make(map[string]int)

	for _, s := range spans {
		svcCounts[s.Service]++
		accum, ok := svcAccum[s.Service]
		if !ok {
			accum = make(map[string]*attrAccum)
			svcAccum[s.Service] = accum
		}
		for k, v := range s.Attributes {
			a, ok := accum[k]
			if !ok {
				accum[k] = &attrAccum{value: v, count: 1, constant: true}
			} else {
				a.count++
				if a.value != v {
					a.constant = false
				}
			}
		}
	}

	result := make(map[string]map[string]string)
	for svc, accum := range svcAccum {
		total := svcCounts[svc]
		attrs := make(map[string]string)
		for k, a := range accum {
			if a.constant && a.count == total {
				attrs[k] = a.value
			}
		}
		if len(attrs) > 0 {
			result[svc] = attrs
		}
	}
	return result
}

// computeWindow returns the time window in seconds between first and last root spans.
func computeWindow(trees []*TraceTree) float64 {
	var rootTimes []time.Time
	for _, tree := range trees {
		for _, root := range tree.Roots {
			rootTimes = append(rootTimes, root.Span.StartTime)
		}
	}
	if len(rootTimes) < 2 {
		return 0
	}
	sort.Slice(rootTimes, func(i, j int) bool { return rootTimes[i].Before(rootTimes[j]) })
	window := rootTimes[len(rootTimes)-1].Sub(rootTimes[0])
	return window.Seconds()
}

// validateRoundTrip checks that the generated YAML parses and validates correctly.
func validateRoundTrip(yamlBytes []byte) error {
	cfg, err := synth.ParseConfig(yamlBytes)
	if err != nil {
		return err
	}
	return synth.ValidateConfig(cfg)
}
