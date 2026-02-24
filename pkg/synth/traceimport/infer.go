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
	Format    Format
	MinTraces int
	Warnings  io.Writer // defaults to os.Stderr
}

// Import reads trace spans, analyses them, and produces a synth YAML config.
func Import(r io.Reader, opts Options) ([]byte, error) {
	if opts.Warnings == nil {
		opts.Warnings = os.Stderr
	}
	if opts.MinTraces == 0 {
		opts.MinTraces = 1
	}

	// Step 1: Parse spans
	spans, err := ParseSpans(r, opts.Format)
	if err != nil {
		return nil, err
	}

	// Step 2: Build trace trees
	trees := BuildTrees(spans, opts.Warnings)

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

	// Step 4: Infer service-level constant attributes
	serviceAttrs := inferServiceAttributes(spans)

	// Step 5: Compute traffic rate window
	windowSecs := computeWindow(trees)

	// Step 6: Marshal to YAML
	yamlBytes, err := MarshalConfig(collector, serviceAttrs, traceCount, len(spans), windowSecs)
	if err != nil {
		return nil, err
	}

	// Step 7: Round-trip validation
	if err := validateRoundTrip(yamlBytes); err != nil {
		return nil, fmt.Errorf("round-trip validation failed (this is a bug): %w", err)
	}

	return yamlBytes, nil
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
	f, err := os.CreateTemp("", "synth-infer-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck // best-effort cleanup of temp file

	if _, err := f.Write(yamlBytes); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	cfg, err := synth.LoadConfig(f.Name())
	if err != nil {
		return err
	}
	return synth.ValidateConfig(cfg)
}
