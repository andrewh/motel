// Public trace-generation API
// Emits a topology's traces through a caller-provided TracerProvider so
// pipeline tests and other embedders can drive generation without the engine.
package synth

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// GenerateOptions configures GenerateTraces.
type GenerateOptions struct {
	// Traces is the number of traces to generate. Zero generates nothing.
	Traces int

	// Seed makes generation reproducible: trace i derives its RNG from
	// Seed+i, so the same seed replays the same root choices and span
	// structure. Zero picks a random seed.
	Seed uint64

	// MaxSpansPerTrace bounds the spans emitted per trace.
	// Zero applies DefaultMaxSpansPerTrace.
	MaxSpansPerTrace int
}

// TracerProviderSource adapts a trace.TracerProvider into a TracerSource that
// names each tracer after the service it emits spans for. It accepts the API
// interface rather than the SDK type, so any provider works — an SDK provider
// wired to OTLP, an in-memory test exporter, or a no-op. A nil provider yields
// a nil TracerSource, which GenerateTraces rejects with an error.
func TracerProviderSource(tp trace.TracerProvider) TracerSource {
	if tp == nil {
		return nil
	}
	return func(serviceName string) trace.Tracer { return tp.Tracer(serviceName) }
}

// GenerateTraces emits opts.Traces traces from topo through tracers and
// returns generation statistics. It is exporter-agnostic: the caller owns the
// TracerProvider behind tracers and is responsible for flushing and shutting
// it down after generation. Use TracerProviderSource to wrap a TracerProvider.
//
// Traces are generated back-to-back with no traffic pacing, scenarios, or
// simulation state; each starts from a root chosen by the trace's own
// seed-derived RNG. Generation stops early when ctx is cancelled, returning
// the statistics accumulated so far alongside the context's error.
func GenerateTraces(ctx context.Context, topo *Topology, tracers TracerSource, opts GenerateOptions) (*Stats, error) {
	if tracers == nil {
		return nil, fmt.Errorf("no tracer source provided")
	}
	if topo == nil {
		return nil, fmt.Errorf("no topology provided")
	}
	if len(topo.Roots) == 0 {
		return nil, fmt.Errorf("no root operations to generate traces from")
	}

	seed := opts.Seed
	if seed == 0 {
		seed = rand.Uint64() //nolint:gosec // not security-sensitive
	}
	spanLimit := opts.MaxSpansPerTrace
	if spanLimit <= 0 {
		spanLimit = DefaultMaxSpansPerTrace
	}

	engine := &Engine{
		Topology:     topo,
		Tracers:      tracers,
		linkRegistry: newSpanContextRegistry(topo),
	}

	var stats Stats
	startTime := time.Now()
	for i := range opts.Traces {
		select {
		case <-ctx.Done():
			engine.finaliseStats(&stats, startTime)
			return &stats, ctx.Err()
		default:
		}

		engine.Rng = rand.New(rand.NewPCG(seed+uint64(i), 0)) //nolint:gosec // not security-sensitive
		root := topo.Roots[engine.Rng.IntN(len(topo.Roots))]

		spanCount := 0
		_, rootErr := engine.walkTrace(ctx, root, nil, time.Now(), 0, nil, nil, &stats, &spanCount, spanLimit, false, false)
		stats.Traces++
		if rootErr {
			stats.FailedTraces++
		}
		if spanCount >= spanLimit {
			stats.SpansBounded++
		}
	}

	engine.finaliseStats(&stats, startTime)
	return &stats, nil
}
