// Benchmarks for the simulation engine hot paths
// Run with: go test -bench=. -benchmem ./pkg/synth/
package synth

import (
	"context"
	"io"
	"math/rand/v2"
	"testing"
	"time"

	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func benchmarkTopology() *Config {
	return &Config{
		Services: []ServiceConfig{
			{
				Name:       "gateway",
				Attributes: map[string]string{"deployment.environment": "production"},
				Operations: []OperationConfig{{
					Name:     "GET /api/users",
					Duration: "5ms +/- 2ms",
					Attributes: map[string]AttributeValueConfig{
						"http.method":      {Value: "GET"},
						"http.route":       {Value: "/api/users"},
						"http.status_code": {Values: map[any]int{"200": 90, "404": 5, "500": 5}},
					},
					Calls: []CallConfig{{Target: "backend.query"}, {Target: "cache.get"}},
				}},
			},
			{
				Name: "backend",
				Operations: []OperationConfig{{
					Name:     "query",
					Duration: "15ms +/- 5ms",
					Attributes: map[string]AttributeValueConfig{
						"db.system":    {Value: "postgresql"},
						"db.operation": {Values: map[any]int{"SELECT": 80, "INSERT": 15, "UPDATE": 5}},
					},
					Calls: []CallConfig{{Target: "database.execute"}},
				}},
			},
			{
				Name: "database",
				Operations: []OperationConfig{{
					Name:     "execute",
					Duration: "8ms +/- 3ms",
					Attributes: map[string]AttributeValueConfig{
						"db.rows_affected": {Range: []int64{0, 100}},
					},
				}},
			},
			{
				Name: "cache",
				Operations: []OperationConfig{{
					Name:     "get",
					Duration: "2ms +/- 1ms",
					Attributes: map[string]AttributeValueConfig{
						"cache.hit": {Probability: ptrFloat64(0.8)},
					},
				}},
			},
		},
		Traffic: TrafficConfig{Rate: "100/s"},
	}
}

func ptrFloat64(v float64) *float64 { return &v }

func BenchmarkWalkTrace(b *testing.B) {
	cfg := benchmarkTopology()
	topo, err := BuildTopology(cfg)
	if err != nil {
		b.Fatal(err)
	}

	tp := sdktrace.NewTracerProvider()
	b.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for benchmarking
	engine := &Engine{
		Topology: topo,
		Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
		Rng:      rng,
	}

	rootOp := topo.Roots[0]
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var stats Stats
		spanCount := 0
		engine.walkTrace(context.Background(), rootOp, now, 0, nil, nil, &stats, &spanCount, DefaultMaxSpansPerTrace)
	}
	b.StopTimer()

	// Report spans per iteration for reference
	var stats Stats
	spanCount := 0
	engine.walkTrace(context.Background(), rootOp, time.Now(), 0, nil, nil, &stats, &spanCount, DefaultMaxSpansPerTrace)
	b.ReportMetric(float64(stats.Spans), "spans/trace")
}

func BenchmarkAttributeGeneration(b *testing.B) {
	rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for benchmarking

	b.Run("StaticValue", func(b *testing.B) {
		gen := &StaticValue{Value: "GET"}
		b.ReportAllocs()
		for range b.N {
			gen.Generate(rng)
		}
	})

	b.Run("WeightedChoice", func(b *testing.B) {
		gen, err := newWeightedChoice(map[any]int{"200": 90, "404": 5, "500": 5})
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for range b.N {
			gen.Generate(rng)
		}
	})

	b.Run("SequenceValue", func(b *testing.B) {
		gen := &SequenceValue{Pattern: "req-{n}"}
		b.ReportAllocs()
		for range b.N {
			gen.Generate(rng)
		}
	})

	b.Run("RangeValue", func(b *testing.B) {
		gen := &RangeValue{Min: 0, Max: 100}
		b.ReportAllocs()
		for range b.N {
			gen.Generate(rng)
		}
	})

	b.Run("NormalValue", func(b *testing.B) {
		gen := &NormalValue{Mean: 50.0, StdDev: 10.0}
		b.ReportAllocs()
		for range b.N {
			gen.Generate(rng)
		}
	})

	b.Run("BoolValue", func(b *testing.B) {
		gen := &BoolValue{Probability: 0.8}
		b.ReportAllocs()
		for range b.N {
			gen.Generate(rng)
		}
	})
}

func BenchmarkEngineRun(b *testing.B) {
	rates := []string{"100/s", "1000/s", "5000/s", "10000/s"}

	for _, rate := range rates {
		b.Run(rate, func(b *testing.B) {
			cfg := benchmarkTopology()
			cfg.Traffic.Rate = rate
			topo, err := BuildTopology(cfg)
			if err != nil {
				b.Fatal(err)
			}
			pattern, err := NewTrafficPattern(cfg.Traffic)
			if err != nil {
				b.Fatal(err)
			}

			tp := sdktrace.NewTracerProvider()
			b.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				engine := &Engine{
					Topology: topo,
					Traffic:  pattern,
					Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
					Rng:      rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for benchmarking
					Duration: 1 * time.Second,
					State:    NewSimulationState(topo),
				}
				stats, err := engine.Run(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(stats.SpansPerSec), "spans/sec")
				b.ReportMetric(float64(stats.Spans), "spans/run")
			}
		})
	}
}

func BenchmarkStdoutVsNoop(b *testing.B) {
	cfg := benchmarkTopology()
	cfg.Traffic.Rate = "1000/s"
	topo, err := BuildTopology(cfg)
	if err != nil {
		b.Fatal(err)
	}
	pattern, err := NewTrafficPattern(cfg.Traffic)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("noop", func(b *testing.B) {
		tp := sdktrace.NewTracerProvider()
		b.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			engine := &Engine{
				Topology: topo,
				Traffic:  pattern,
				Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
				Rng:      rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for benchmarking
				Duration: 1 * time.Second,
				State:    NewSimulationState(topo),
			}
			stats, err := engine.Run(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(stats.SpansPerSec), "spans/sec")
		}
	})

	b.Run("stdout-discard", func(b *testing.B) {
		exporter, err := stdouttrace.New(stdouttrace.WithWriter(io.Discard))
		if err != nil {
			b.Fatal(err)
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		b.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			engine := &Engine{
				Topology: topo,
				Traffic:  pattern,
				Tracers:  func(name string) trace.Tracer { return tp.Tracer(name) },
				Rng:      rand.New(rand.NewPCG(42, 0)), //nolint:gosec // deterministic seed for benchmarking
				Duration: 1 * time.Second,
				State:    NewSimulationState(topo),
			}
			stats, err := engine.Run(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(stats.SpansPerSec), "spans/sec")
		}
	})
}
