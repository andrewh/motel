# Design: Collector Pipeline Testing Architecture

This document resolves the foundational design questions for property testing
OpenTelemetry Collector pipelines with motel as a signal generator (issue 63),
and describes the proof-of-concept that demonstrates the recommended approach.

## Problem

motel generates synthetic OTLP from a topology model. A collector pipeline
(sampling, filtering, routing, batching, attribute processing) transforms
telemetry in flight. These configs are combinatorial: processors compose,
sampling interacts with filtering, tail-based sampling depends on whole traces.
Hand-written test traces cannot cover the interaction surface.

The premise of the epic is to feed a *real* pipeline diverse synthetic signals
and assert invariants on its output. The system under test is real
infrastructure — a collector binary running a real config — not the topology
model, which sidesteps the "test a model against itself" problem. motel's
generators supply the variety (deep traces, wide fan-out, mixed error/success,
varying attribute sets) and [rapid](https://github.com/flyingmutant/rapid)'s
shrinking finds the minimal trace that breaks a pipeline.

This design answers four questions: scope, output capture, pipeline
agnosticism, and lifecycle.

## Recommended approach

A **harness library plus composed property tests**, both living in the motel
repo. The harness manages a real collector subprocess and captures its OTLP
output through an in-process receiver. Invariants are ordinary Go tests written
with rapid, generating topologies with motel's engine.

```
                  OTLP/HTTP                         OTLP/HTTP
  motel engine  ───────────►  otelcol subprocess  ───────────►  Sink
  (in-process)   (SDK export)  (real config)       (export)      (in-process
       │                                                          receiver)
       └── records sent span IDs ──────────────────► assert invariant ◄──┘
            (second in-memory exporter)               over both sets
```

The proof-of-concept lives in
[`pkg/pipelinetest`](../../pkg/pipelinetest) (the reusable I/O harness) and
[`pkg/synth/pipeline_test.go`](../../pkg/synth/pipeline_test.go) (the
invariants). It passes the trivial conservation invariant over 100 generated
topologies and demonstrates that the harness detects a lossy pipeline.

## Scope: library, not a subcommand

**Recommendation: a harness library in the motel repo, with invariants written
as `go test` targets. Not a `motel test-pipeline` subcommand.**

motel's identity is a generator: a standalone CLI with no server and no
persistent state. Pipeline testing is a distinct concern with a different
lifecycle — orchestrating a collector subprocess, capturing its output, and
asserting invariants. Three reasons to keep it out of the CLI:

- The assertions *are* code. An invariant like "every span sent through a
  pass-through pipeline is received" is a predicate over two span sets, not a
  flag on a command. It belongs in a `_test.go` file, exactly like motel's
  existing property and fuzz suite (see
  [property-testing.md](../explanation/property-testing.md)). This reuses the
  rapid dependency and the established testing idioms rather than inventing a
  config language for invariants.
- It reuses motel as a library, which is what the epic calls for ("motel as a
  signal generator"). The harness imports the engine; it does not extend the
  binary.
- Keeping collector orchestration out of the generator avoids coupling the
  shipped binary to a specific system under test and its dependencies.

This is option (b)/(c) from the issue — a library that uses motel, with tests
users (and we) compose — rather than option (a), a motel feature.

## Output capture: embedded OTLP receiver

**Recommendation: an in-process OTLP receiver (the `Sink`) that the pipeline
exports to.**

The pipeline's last exporter points at a small in-process server that records
every span it receives. Assertions run in the same process against captured
protobuf spans. The PoC implements OTLP/HTTP: the collector's `otlphttp`
exporter POSTs a binary-protobuf `ExportTraceServiceRequest` to `/v1/traces`,
per the [OTLP specification](https://opentelemetry.io/docs/specs/otlp/).

The alternatives were rejected:

- **File exporter.** Requires polling files for completion, races with the
  collector's flush, depends on the collector-specific `file` exporter, and
  forces JSON parsing of the output. More moving parts, more flakiness.
- **In-process collector (collector-contrib as a library).** Drags the entire
  collector dependency tree into motel's build, pins the harness to one
  collector version compiled in, and conflates the test harness with the
  system under test. The whole point is to test a *real* collector binary and
  its config; embedding the collector undermines that.

The receiver depends only on the OTLP protocol
([`go.opentelemetry.io/proto/otlp`](https://pkg.go.dev/go.opentelemetry.io/proto/otlp),
already a dependency), not on any collector internals.

## Pipeline agnosticism: anything that speaks OTLP

**Recommendation: target the OTLP contract, with the OTel Collector as the
reference implementation.**

The harness contract is "OTLP in (from motel), OTLP out (to the Sink)". Any
system that consumes and emits OTLP can be the system under test — the OTel
Collector, a custom Go pipeline, or another OTLP-speaking processor. The
collector config is an input to the test, not a fixed part of the harness:
`pipelinetest.Start` takes the pipeline config as a template and fills in only
the connection details (receiver port, sink URL, health port).

Targeting the collector *specifically* would let the harness reach into
collector internals, but that trades away both agnosticism and the realism of
running the actual binary. The OTLP contract keeps both.

## Lifecycle: the harness manages the collector

**Recommendation: the harness starts the collector as a subprocess and stops it
on cleanup.**

`pipelinetest.Start` allocates ephemeral loopback ports, renders the config to
a temp file, starts the collector binary, and polls the
[`health_check`](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/extension/healthcheckextension)
extension until the pipeline reports healthy. The test registers `Stop` with
`t.Cleanup`, which kills the process and removes the temp directory. The binary
is resolved from `MOTEL_COLLECTOR_BIN` or `otelcol` on `PATH`; tests skip when
neither is present, so the suite stays green without a collector.

Expecting an already-running collector was rejected: state would leak across
property iterations, and config-varying tests (issue 77, swarm over processor
configurations) need a fresh collector per config. A managed subprocess gives
each test a clean, reproducible pipeline.

A fixed config can reuse one collector across many rapid draws — the PoC's
property test starts the collector once and resets the sink between draws.
Config-varying tests pay one subprocess start per config.

## Proof of concept

Three tests in [`pkg/synth/pipeline_test.go`](../../pkg/synth/pipeline_test.go)
exercise the architecture against a real `otelcol` v0.128.0:

- **`TestPipeline_AllSpansRoundTrip`** — generates 20 traces from a fixed
  topology through a pass-through pipeline (`otlp` → `otlphttp`, no processors)
  and asserts every span sent is received. The trivial conservation invariant.
- **`TestPipeline_AllSpansRoundTripProperty`** — the property-testing
  direction: for any topology that `genSimpleConfig` generates, the same
  pass-through pipeline conserves every span. Passes 100 generated topologies
  reusing one collector.
- **`TestPipeline_SamplingDropsSpans`** — the negative control. A 50%
  `probabilistic_sampler` keeps some spans but not all, and every survivor is a
  span that was actually sent. This proves the harness observes real pipeline
  behaviour rather than echoing its input, and previews the sampling invariants
  of issue 74.

Sent span identities come from a second in-memory exporter attached to the same
`TracerProvider`, so each test knows exactly which `(trace ID, span ID)` pairs
it pushed and can compare them against what the sink captured.

Run them with a collector available:

```sh
# Uses otelcol on PATH, or set MOTEL_COLLECTOR_BIN
go test ./pkg/synth/ -run TestPipeline -v
```

The sink decode path also has a standalone unit test
([`pkg/pipelinetest/sink_test.go`](../../pkg/pipelinetest/sink_test.go)) that
needs no collector, so the harness has always-on coverage.

## Productionisation note

The PoC drives generation from inside `package synth` because it reuses the
unexported `walkTrace`. To let invariants and the harness live entirely outside
`package synth` (which the later sub-issues will want), motel should expose a
small public generation API — for example, emitting a topology's traces into a
caller-provided `TracerProvider`. That is the natural next step and is tracked
with the invariant work rather than this design.

## What later issues build on

- **Issue 74 (sampling trace integrity).** Invariants over trace completeness
  under head and tail sampling — a sampled trace keeps all or none of its
  spans; tail sampling preserves whole traces. `TestPipeline_SamplingDropsSpans`
  is the first step.
- **Issue 75 (filtering and routing).** Needs a richer collector build (the
  reference binary lacks a routing connector) and invariants over which spans
  survive a filter and where routed spans land.
- **Issue 76 (batching and ordering).** Invariants over batch boundaries and
  span ordering; the sink already records arrival order.
- **Issue 77 (swarm over processor configs).** Generates collector configs as
  well as topologies, starting a collector per config. The `Start` config
  template is the seam.

## Limitations

- The receiver speaks OTLP/HTTP only. A gRPC receiver can be added if a system
  under test exports OTLP/gRPC exclusively.
- Free-port allocation has an inherent time-of-check/time-of-use race between
  closing the probe listener and the collector binding the port. Acceptable for
  a test harness; a retry-on-bind-failure loop would remove it.
- Conservation invariants wait until the sink reaches the expected span count.
  Lossy or transforming pipelines have no such fixed point, so those tests wait
  a bounded settle period instead. Later invariants should express "eventually
  received within a bound" explicitly to stay deterministic.
