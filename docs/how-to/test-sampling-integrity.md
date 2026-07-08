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

The invariants are implemented in `pkg/synth/pipeline_sampling_test.go` as
`assertParentsKept`, `assertWholeTraces`, and `assertNoFabrication` (received
spans must be a subset of sent spans), plus a replay test for consistency.
motel generates the traces — varying depth, fan-out, and error mix — and
[rapid](https://github.com/flyingmutant/rapid) shrinks any violation to the
minimal trace shape that triggers it.

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

The tests render these configs themselves (filling in ports and the sink URL),
but the processor blocks are what you would deploy. The head-sampling tests
use `probabilistic_sampler` pinned to `hash_seed` mode, which makes the
decision a pure function of the trace ID — deterministic across runs and
identical for every span of a trace:

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

`pipelinetest.Start` takes the collector config as a template, so a test for
your production sampling policy is the existing test with a different
processor block. Copy one of the pipeline constants from
`pkg/synth/pipeline_sampling_test.go`, substitute your `tail_sampling`
policies or sampler settings, and keep the assertions:

```go
sink, collector := startPipeline(t, myProductionPipeline)

topo := loadTopology(t, passthroughTopology)
sent := generateAndCapture(t, topo, collector.OTLPEndpoint, 40, 7, nil)
spans := sink.WaitSettled(2*time.Second, 30*time.Second)
received := receivedKeys(spans)

assertNoFabrication(t, sentKeys(sent), received)
assertParentsKept(t, spans)
assertWholeTraces(t, sent, received)
```

Two details matter when adapting this:

- **There is no exact span count to wait for.** A sampler drops spans, so the
  sink cannot wait for "all spans arrived". `Sink.WaitSettled(idle, max)` is
  the bounded "eventually received" assertion: it returns once no new span has
  arrived for `idle`, capped at `max`.
- **Choose `idle` longer than the pipeline's largest internal delay.** A tail
  sampler is silent for `decision_wait` while traces sit in its buffer; an
  `idle` shorter than that mistakes deciding for drained.

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
