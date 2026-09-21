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
	YAML     []byte
	Evidence *ImportEvidence
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

	// Span attributes are assessed per operation after latency inference.
	// Resource metadata is reported as omitted, rather than promoted.

	// Step 5: Compute traffic rate window
	windowSecs := computeWindow(trees)

	// Step 6: Marshal to YAML
	yamlBytes, err := MarshalConfig(collector, nil, traceCount, len(spans), windowSecs)
	if err != nil {
		return Result{}, err
	}

	// Step 7: Round-trip validation. Parsing and structural validation guard
	// against serialisation bugs in motel itself, so those failures are flagged
	// as bugs. Topology construction additionally runs cycle detection; because
	// self-nested spans are folded during stats collection, a remaining cycle
	// reflects a genuinely cyclic call pattern in the source traces that motel's
	// acyclic model cannot represent — a property of the input, not a bug.
	cfg, err := synth.ParseConfig(yamlBytes)
	if err != nil {
		return Result{}, fmt.Errorf("round-trip parse failed (this is a bug): %w", err)
	}
	if err := synth.ValidateConfig(cfg); err != nil {
		return Result{}, fmt.Errorf("round-trip validation failed (this is a bug): %w", err)
	}
	if _, err := synth.BuildTopology(cfg, nil); err != nil {
		return Result{}, fmt.Errorf("inferred topology is not valid: %w\n\n"+
			"this typically means the source traces contain a cyclic call pattern "+
			"(an operation that, directly or transitively, calls itself across "+
			"services), which motel's acyclic model cannot represent", err)
	}

	evidence, err := assessImport(collector, trees, cfg, opts.MinTraces)
	if err != nil {
		return Result{}, err
	}
	yamlBytes, err = attachEvidence(yamlBytes, evidence)
	if err != nil {
		return Result{}, err
	}
	if err := validateRoundTrip(yamlBytes); err != nil {
		return Result{}, fmt.Errorf("validating topology with import evidence: %w", err)
	}
	return Result{
		Evidence:   evidence,
		YAML:       yamlBytes,
		TraceCount: traceCount,
		SpanCount:  len(spans),
	}, nil
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

// validateRoundTrip checks that the generated YAML parses, validates, and builds
// into a topology. Building runs the same checks as `motel validate` — notably
// cycle detection — so the importer never emits YAML that the validate command
// would reject.
func validateRoundTrip(yamlBytes []byte) error {
	cfg, err := synth.ParseConfig(yamlBytes)
	if err != nil {
		return err
	}
	if err := synth.ValidateConfig(cfg); err != nil {
		return err
	}
	_, err = synth.BuildTopology(cfg, nil)
	return err
}
