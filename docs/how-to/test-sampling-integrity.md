# Test Sampling Trace Integrity

This guide shows how to verify that a sampling processor in an OpenTelemetry
Collector pipeline never breaks trace structure — no orphaned spans, no
partially kept traces, no unstable keep/drop decisions. The checks run as Go
property tests that drive motel-generated traces through a real collector
binary, using the harness described in the
[collector pipeline testing design](../research/collector-pipeline-testing.md).

## The invariants

Tail-based and probabilistic sampling can break trace integrity in ways that
depend on timing, batch boundaries, and trace shape — exactly the bugs that
hand-written test traces miss. Three invariants pin the correct behaviour:

- **Parent-child preservation.** If a child span is kept, its parent span is
  also kept. A pipeline that keeps a child while dropping its parent produces
  orphaned spans that render as broken traces in every backend.
- **Trace completeness.** If any span from a trace is kept, all spans from
  that trace are kept. Samplers that decide per trace — head samplers keyed on
  the trace ID, and all tail samplers — must keep or drop traces whole.
- **Sampling consistency.** Replaying the same trace through the same sampler
  produces the same keep/drop decision. This holds for any deterministic
  sampler, such as `probabilistic_sampler` in `hash_seed` mode.

The invariants are exported from `pkg/pipelinetest` as `CheckParentsKept`,
`CheckWholeTraces`, and `CheckNoFabrication` (received spans must be a subset of
sent spans), so any test — ours or yours — can assert them over a sink's output;
`pkg/synth/pipeline_sampling_test.go` drives them against a real collector, plus
a replay test for consistency. motel generates the traces — varying depth,
fan-out, and error mix — and [rapid](https://github.com/flyingmutant/rapid)
shrinks any violation to the minimal trace shape that triggers it.

## What you need

- The motel repository (the checks are `go test` targets, not a CLI feature)
- A collector binary: the reference `otelcol` build covers the head-sampling
  tests; the tail-sampling test needs a build that includes the
  `tail_sampling` processor, such as `otelcol-contrib`

## 1. Point the tests at a collector

The harness resolves the collector binary from `MOTEL_COLLECTOR_BIN`, falling
back to `otelcol` on `PATH`. Tests skip when neither is present.

```sh
export MOTEL_COLLECTOR_BIN=/path/to/otelcol-contrib
```

## 2. Run the invariant tests

```sh
go test ./pkg/synth/ -run TestSampling -v
```

The suite contains:

- **`TestSampling_TraceIntegrity`** — a fixed multi-service topology through a
  50% head sampler: survivors are a subset of what was sent, no orphans, whole
  traces only.
- **`TestSampling_TraceIntegrityProperty`** — the same invariants for 100
  rapid-generated topologies, reusing one collector.
- **`TestSampling_DeterministicDecisions`** — replays a byte-identical span
  set twice (a seeded ID generator fixes the trace and span IDs) and asserts
  both runs keep exactly the same spans.
- **`TestSampling_TailWholeTraces`** — trace completeness under
  `tail_sampling`, where buffering bugs would surface as partially kept
  traces. Skips unless the collector build advertises the processor.

## Example collector configs

The tests build these configs with `pipelinetest.TracesConfig`, which wraps a
processors block in the shared receiver/exporter/health-check skeleton and fills
in ports and the sink URL. The processor blocks below are what you would deploy.
The head-sampling tests use `probabilistic_sampler` pinned to `hash_seed` mode,
which makes the decision a pure function of the trace ID — deterministic across
runs and identical for every span of a trace:

```yaml
processors:
  probabilistic_sampler:
    sampling_percentage: 50
    mode: hash_seed
    hash_seed: 22

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [probabilistic_sampler]
      exporters: [otlphttp]
```

The tail-sampling test buffers whole traces before deciding:

```yaml
processors:
  tail_sampling:
    decision_wait: 1s
    policies:
      - name: keep-half
        type: probabilistic
        probabilistic:
          sampling_percentage: 50

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [tail_sampling]
      exporters: [otlphttp]
```

## 3. Check your own sampling config

The harness and the invariant checks are exported from `pkg/pipelinetest`, so a
test for your own sampling policy composes them directly — no need to fork the
motel test files. `pipelinetest.TracesConfig` builds a single-pipeline config
from a processors block; `pipelinetest.Start` runs a collector against it and
points it at a `Sink`; the `Check*` functions assert the invariants over the
sink's output against a `pipelinetest.Sent` set you build while generating:

```go
sink := pipelinetest.NewSink()
defer sink.Close()

// Your sampling policy is the only thing that changes.
config := pipelinetest.TracesConfig(`  tail_sampling:
    decision_wait: 5s
    policies:
      - name: errors
        type: status_code
        status_code:
          status_codes: [ERROR]
`, "tail_sampling")

collector, err := pipelinetest.Start(sink, config)
if err != nil {
	t.Fatal(err)
}
defer func() { _ = collector.Stop() }()

// Generate traces through an OTLP exporter aimed at the collector, and record
// their identities from a second in-memory exporter on the same provider.
captured := tracetest.NewInMemoryExporter()
otlp, _ := otlptracehttp.New(ctx,
	otlptracehttp.WithEndpoint(collector.OTLPEndpoint),
	otlptracehttp.WithInsecure())
tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(otlp), sdktrace.WithSyncer(captured))
if _, err := synth.GenerateTraces(ctx, topo, synth.TracerProviderSource(tp),
	synth.GenerateOptions{Traces: 40, Seed: 7}); err != nil {
	t.Fatal(err)
}
_ = tp.ForceFlush(ctx)

sent := pipelinetest.NewSent()
for _, s := range captured.GetSpans() {
	tid, sid := s.SpanContext.TraceID(), s.SpanContext.SpanID()
	sent.Add(tid[:], sid[:])
}

// A sampler drops spans, so wait for the pipeline to settle rather than for a
// fixed count, then assert the invariants over the survivors.
spans := sink.WaitSettled(6*time.Second, 30*time.Second)
for _, check := range []error{
	pipelinetest.CheckNoFabrication(sent, spans),
	pipelinetest.CheckParentsKept(spans),
	pipelinetest.CheckWholeTraces(sent, spans),
} {
	if check != nil {
		t.Fatal(check)
	}
}
```

Two details matter when adapting this:

- **There is no exact span count to wait for.** A sampler drops spans, so the
  sink cannot wait for "all spans arrived". `Sink.WaitSettled(idle, max)` is
  the bounded "eventually received" assertion: it returns once no new span has
  arrived for `idle`, capped at `max`.
- **Choose `idle` longer than the pipeline's largest internal delay.** A tail
  sampler is silent while traces sit in its buffer for `decision_wait` plus its
  internal policy-evaluation tick (~1s); an `idle` shorter than that total
  mistakes deciding for drained. Leave headroom for scheduling jitter — the
  suite waits 4s against a 1s `decision_wait`.

Policies that key on trace properties rather than the trace ID — latency,
status code, attributes — still satisfy all three invariants: the decision is
made once per trace, so completeness and parent preservation follow, and the
same trace always matches the same policies. Use a topology with a realistic
mix of durations, error rates, and attributes (see
[Test tail sampling policies](test-tail-sampling.md)) so every policy branch
is exercised.

## Further reading

- [Collector pipeline testing design](../research/collector-pipeline-testing.md)
  -- the harness architecture these checks build on
- [Test tail sampling policies](test-tail-sampling.md) -- manually exploring
  what a tail sampling config keeps, with a richer example topology
- [Property testing in motel](../explanation/property-testing.md) -- rationale
  and patterns for the rapid-based test suite
