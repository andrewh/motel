// Property-based tests for the synth engine using pgregory.net/rapid
// Covers topology conformance, span nesting, error cascading, and stats consistency
package synth

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"pgregory.net/rapid"
)

// --- Generators ---

// genSimpleConfig generates a valid Config with 1-4 services, each with 1-3 operations,
// and a DAG of calls between them (no cycles).
func genSimpleConfig(t *rapid.T) *Config {
	type svcOp struct{ svc, op string }

	nSvcs := rapid.IntRange(1, 4).Draw(t, "nSvcs")
	svcNames := make([]string, nSvcs)
	for i := range nSvcs {
		svcNames[i] = fmt.Sprintf("svc%d", i)
	}

	var allOps []svcOp
	svcs := make([]ServiceConfig, nSvcs)
	for i, svcName := range svcNames {
		nOps := rapid.IntRange(1, 3).Draw(t, fmt.Sprintf("nOps%d", i))
		ops := make([]OperationConfig, nOps)
		for j := range nOps {
			opName := fmt.Sprintf("op%d", j)
			dur := rapid.IntRange(1, 100).Draw(t, fmt.Sprintf("dur%d_%d", i, j))
			errPct := rapid.IntRange(0, 100).Draw(t, fmt.Sprintf("errPct%d_%d", i, j))
			ops[j] = OperationConfig{
				Name:     opName,
				Duration: fmt.Sprintf("%dms", dur),
			}
			if errPct > 0 {
				ops[j].ErrorRate = fmt.Sprintf("%d%%", errPct)
			}
			allOps = append(allOps, svcOp{svcName, opName})
		}
		svcs[i] = ServiceConfig{Name: svcName, Operations: ops}
	}

	// Add calls: each operation can call operations from later services only (ensures DAG)
	for i := range svcs {
		for j := range svcs[i].Operations {
			// Only call operations in services with higher index
			var targets []svcOp
			for _, so := range allOps {
				for k, sn := range svcNames {
					if k > i && sn == so.svc {
						targets = append(targets, so)
					}
				}
			}
			if len(targets) == 0 {
				continue
			}
			nCalls := rapid.IntRange(0, min(len(targets), 2)).Draw(t, fmt.Sprintf("nCalls%d_%d", i, j))
			if nCalls == 0 {
				continue
			}
			// Shuffle targets and pick first nCalls
			shuffled := rapid.Permutation(targets).Draw(t, fmt.Sprintf("perm%d_%d", i, j))
			calls := make([]CallConfig, nCalls)
			for c := range nCalls {
				calls[c] = CallConfig{Target: shuffled[c].svc + "." + shuffled[c].op}
			}
			svcs[i].Operations[j].Calls = calls
		}
	}

	return &Config{
		Services: svcs,
		Traffic:  TrafficConfig{Rate: "100/s"},
	}
}

// genSeed draws a random seed for the engine's RNG.
func genSeed(t *rapid.T) uint64 {
	return rapid.Uint64().Draw(t, "seed")
}

// walkOnce builds a topology from config, walks one trace, and returns the exporter's spans.
func walkOnce(t *rapid.T, cfg *Config) (*Topology, []tracetest.SpanStub, *Stats) {
	topo, err := BuildTopology(cfg)
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	if len(topo.Roots) == 0 {
		t.Skip("no root operations")
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	seed := genSeed(t)
	rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // property test

	engine := &Engine{
		Topology: topo,
		Provider: tp,
		Rng:      rng,
	}

	rootOp := topo.Roots[rng.IntN(len(topo.Roots))]
	now := time.Now()
	var stats Stats
	engine.walkTrace(context.Background(), rootOp, now, 0, nil, &stats, new(int), DefaultMaxSpansPerTrace)

	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}

	return topo, exporter.GetSpans(), &stats
}

// --- Topology conformance ---

func TestProperty_Engine_SpansMatchTopology(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, spans, _ := walkOnce(t, cfg)

		knownOps := make(map[string]bool)
		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				knownOps[op.Ref] = true
			}
		}

		for _, span := range spans {
			svcName := ""
			opName := span.Name
			for _, attr := range span.Attributes {
				if string(attr.Key) == "synth.service" {
					svcName = attr.Value.AsString()
				}
			}
			ref := svcName + "." + opName
			if !knownOps[ref] {
				t.Fatalf("span %q not in topology (known: %v)", ref, knownOps)
			}
		}
	})
}

// --- Call graph correctness ---

func TestProperty_Engine_CallGraphMatchesTopology(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, spans, _ := walkOnce(t, cfg)

		// Build a set of valid caller→callee pairs from topology
		validCalls := make(map[string]map[string]bool) // parentRef -> set of childRefs
		for _, svc := range topo.Services {
			for _, op := range svc.Operations {
				targets := make(map[string]bool)
				for _, call := range op.Calls {
					targets[call.Operation.Ref] = true
				}
				validCalls[op.Ref] = targets
			}
		}

		// Index spans by span ID
		spanByID := make(map[trace.SpanID]tracetest.SpanStub)
		for _, s := range spans {
			spanByID[s.SpanContext.SpanID()] = s
		}

		refOf := func(s tracetest.SpanStub) string {
			svcName := ""
			for _, attr := range s.Attributes {
				if string(attr.Key) == "synth.service" {
					svcName = attr.Value.AsString()
				}
			}
			return svcName + "." + s.Name
		}

		for _, s := range spans {
			if !s.Parent.SpanID().IsValid() {
				continue // root span
			}
			parent, ok := spanByID[s.Parent.SpanID()]
			if !ok {
				continue // parent not in this batch (shouldn't happen but safe)
			}
			parentRef := refOf(parent)
			childRef := refOf(s)
			if !validCalls[parentRef][childRef] {
				t.Fatalf("call %s -> %s not in topology", parentRef, childRef)
			}
		}
	})
}

// --- Span nesting ---

func TestProperty_Engine_ChildSpansNestedInParent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		spanByID := make(map[trace.SpanID]tracetest.SpanStub)
		for _, s := range spans {
			spanByID[s.SpanContext.SpanID()] = s
		}

		for _, child := range spans {
			if !child.Parent.SpanID().IsValid() {
				continue
			}
			parent, ok := spanByID[child.Parent.SpanID()]
			if !ok {
				continue
			}
			if child.StartTime.Before(parent.StartTime) {
				t.Fatalf("child %s starts before parent %s", child.Name, parent.Name)
			}
			if parent.EndTime.Before(child.EndTime) {
				t.Fatalf("parent %s (end=%v) ends before child %s (end=%v)",
					parent.Name, parent.EndTime, child.Name, child.EndTime)
			}
		}
	})
}

// --- Positive duration ---

func TestProperty_Engine_SpanDurationsPositive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		for _, s := range spans {
			dur := s.EndTime.Sub(s.StartTime)
			if dur < 0 {
				t.Fatalf("span %s has negative duration %v", s.Name, dur)
			}
		}
	})
}

// --- Stats consistency ---

func TestProperty_Engine_StatsMatchSpans(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, stats := walkOnce(t, cfg)

		if int64(len(spans)) != stats.Spans {
			t.Fatalf("exporter has %d spans, stats says %d", len(spans), stats.Spans)
		}
	})
}

func TestProperty_Engine_ErrorsBounded(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, _, stats := walkOnce(t, cfg)

		if stats.Errors > stats.Spans {
			t.Fatalf("errors %d > spans %d", stats.Errors, stats.Spans)
		}
		if stats.Errors < 0 {
			t.Fatalf("errors %d is negative", stats.Errors)
		}
	})
}

// --- Error cascading ---

func TestProperty_Engine_ErrorCascadesToParent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		spanByID := make(map[trace.SpanID]tracetest.SpanStub)
		for _, s := range spans {
			spanByID[s.SpanContext.SpanID()] = s
		}

		// Build children map
		childrenOf := make(map[trace.SpanID][]tracetest.SpanStub)
		for _, s := range spans {
			if s.Parent.SpanID().IsValid() {
				childrenOf[s.Parent.SpanID()] = append(childrenOf[s.Parent.SpanID()], s)
			}
		}

		for _, parent := range spans {
			children := childrenOf[parent.SpanContext.SpanID()]
			if len(children) == 0 {
				continue
			}
			anyChildErrored := false
			for _, child := range children {
				if child.Status.Code == codes.Error {
					anyChildErrored = true
					break
				}
			}
			if anyChildErrored && parent.Status.Code != codes.Error {
				t.Fatalf("child of %s errored but parent did not cascade", parent.Name)
			}
		}
	})
}

// --- Root span kind ---

func TestProperty_Engine_RootSpanIsServer(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		for _, s := range spans {
			if !s.Parent.SpanID().IsValid() {
				if s.SpanKind != trace.SpanKindServer {
					t.Fatalf("root span %s has kind %v, expected Server", s.Name, s.SpanKind)
				}
			}
		}
	})
}

func TestProperty_Engine_NonRootSpanIsClient(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		for _, s := range spans {
			if s.Parent.SpanID().IsValid() {
				if s.SpanKind != trace.SpanKindClient {
					t.Fatalf("non-root span %s has kind %v, expected Client", s.Name, s.SpanKind)
				}
			}
		}
	})
}

// --- Single trace ---

func TestProperty_Engine_AllSpansShareTraceID(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		_, spans, _ := walkOnce(t, cfg)

		if len(spans) == 0 {
			return
		}
		traceID := spans[0].SpanContext.TraceID()
		for _, s := range spans[1:] {
			if s.SpanContext.TraceID() != traceID {
				t.Fatalf("span %s has trace ID %v, expected %v", s.Name, s.SpanContext.TraceID(), traceID)
			}
		}
	})
}
