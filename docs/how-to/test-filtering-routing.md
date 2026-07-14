# Test Filtering and Routing

This guide shows how to verify that filter and routing stages in an
OpenTelemetry Collector pipeline do exactly what their rules say — no span
silently dropped or leaked by a filter, no trace torn across backends, no
telemetry falling through a routing table into the void. The checks run as Go
property tests that drive motel-generated traces through a real collector
binary, using the harness described in the
[collector pipeline testing design](../research/collector-pipeline-testing.md).

## The invariants

Filter and routing bugs are combinatorial: they emerge from interactions
between rules, attribute values, and trace shapes, so hand-picked test spans
miss them. Three invariants pin the correct behaviour:

- **Filter correctness.** The filter's output is exactly the spans its
  predicate keeps — no false positives (a span dropped that shouldn't match)
  and no false negatives (a matching span that leaks through).
- **Routing consistency.** All spans of a trace arrive at the same backend.
  Attribute-based routing must key on something constant across a trace (a
  resource attribute), or each backend receives fragments no trace view or
  tail-based tool can reassemble.
- **Rule completeness.** Every span sent is delivered to some backend. A
  routing table without a default silently discards whatever matches no rule.

The invariants are exported from `pkg/pipelinetest` as
`CheckFilterCorrectness`, `CheckRoutingConsistency`, and
`CheckRouteCompleteness`, so any test — ours or yours — can assert them;
`pkg/synth/pipeline_filter_test.go` and `pkg/synth/pipeline_routing_test.go`
drive them against a real collector. For filter correctness the test computes
the expected keep/drop partition itself: it knows every span it sent — with
attributes — from the in-memory capture, so applying the filter's predicate
client-side reduces the check to a set comparison. motel generates the traces,
and [rapid](https://github.com/flyingmutant/rapid) shrinks any violation to the
minimal topology that triggers it.

## What you need

- The motel repository (the checks are `go test` targets, not a CLI feature)
- A collector build with the `filter` processor and `routing` connector, such
  as `otelcol-contrib` (tests skip per-component when the binary lacks one —
  the harness probes `components` output)

## 1. Point the tests at a collector

The harness resolves the collector binary from `MOTEL_COLLECTOR_BIN`, falling
back to `otelcol` on `PATH`. Tests skip when neither is present.

```sh
export MOTEL_COLLECTOR_BIN=/path/to/otelcol-contrib
```

## 2. Run the invariant tests

```sh
go test ./pkg/synth/ -run 'TestFilter_|TestRouting_' -v
```

The suite contains:

- **`TestFilter_ExactPartition`** — a fixed topology through a filter dropping
  one service's spans; the output must be exactly the complement.
- **`TestFilter_ErrorSpansProperty`** — filter correctness for 100
  rapid-generated topologies under a status-based filter (drop error spans),
  reusing one collector.
- **`TestFilter_MidTreeDropBreaksLineage`** — the interaction bug the
  structural invariants catch: a filter that is perfectly correct span by span
  still orphans children when its predicate matches mid-tree spans.
- **`TestRouting_WholeTracesPerBackend`** — two tenants' traffic through a
  resource-attribute router: traces arrive whole, nothing falls through, each
  backend receives exactly its tenant's spans.
- **`TestRouting_ConsistencyProperty`** — the routing invariants for 100
  rapid-generated topologies.
- **`TestRouting_SplitByServiceDetected`** — routing on a per-span property
  (the DIY pattern of parallel pipelines with complementary filters) tears
  every multi-service trace apart; the consistency check must detect it.
- **`TestRouting_FallthroughDetected`** — a routing table without
  `default_pipelines` silently discards unmatched tenants; the completeness
  check must detect it.

## Example collector configs

A correct filter stage — drop what the predicate matches, keep everything
else (`pipelinetest.TracesConfig` wraps this into the shared
receiver/exporter skeleton):

```yaml
processors:
  filter:
    error_mode: ignore
    traces:
      span:
        - status.code == STATUS_CODE_ERROR

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [filter]
      exporters: [otlphttp]
```

A correct routing stage keys on a **resource** attribute, constant across each
trace, with a default so nothing falls through. Multi-backend configs use
`pipelinetest.StartMulti`, which exposes each sink positionally as
`{{index .SinkURLs N}}`:

```yaml
connectors:
  routing:
    default_pipelines: [traces/primary]
    table:
      - context: resource
        condition: attributes["tenant"] == "beta"
        pipelines: [traces/secondary]

service:
  pipelines:
    traces/in:
      receivers: [otlp]
      exporters: [routing]
    traces/primary:
      receivers: [routing]
      exporters: [otlphttp/primary]
    traces/secondary:
      receivers: [routing]
      exporters: [otlphttp/secondary]
```

Two misconfigurations the invariants exist to catch:

- **Routing on a per-span property.** Parallel pipelines with complementary
  filters (or a routing rule on a span attribute) send each service's spans to
  a different backend — every cross-service trace is split. `CheckRoutingConsistency`
  reports the first torn trace.
- **A rule table with no default.** Omit `default_pipelines` and spans matching
  no rule vanish without an error. `CheckRouteCompleteness` reports the first
  span that fell through.

## 3. Check your own filter or routing config

The harness and checks compose directly — generate through an OTLP exporter at
the collector, capture what was sent with a second in-memory exporter, and
assert over the sinks (see
[Test sampling trace integrity](test-sampling-integrity.md) for the full
generation boilerplate):

```go
primary, secondary := pipelinetest.NewSink(), pipelinetest.NewSink()
defer primary.Close()
defer secondary.Close()

collector, err := pipelinetest.StartMulti(myRoutingConfig, primary, secondary)
if err != nil {
	t.Fatal(err)
}
defer func() { _ = collector.Stop() }()

// ... generate via synth.GenerateTraces, record identities in `sent`,
// and build per-backend expectations from the captured spans ...

backends := map[string][]*tracepb.Span{
	"primary":   primary.WaitSettled(time.Second, 30*time.Second),
	"secondary": secondary.WaitSettled(time.Second, 30*time.Second),
}
for _, check := range []error{
	pipelinetest.CheckRoutingConsistency(backends),
	pipelinetest.CheckRouteCompleteness(sent, backends),
} {
	if check != nil {
		t.Fatal(check)
	}
}
```

For filter correctness, partition the captured spans with the same predicate
your filter config encodes and compare:

```go
keep, drop := map[string]struct{}{}, map[string]struct{}{}
for _, s := range captured.GetSpans() {
	tid, sid := s.SpanContext.TraceID(), s.SpanContext.SpanID()
	key := pipelinetest.SpanKey(tid[:], sid[:])
	if s.Status.Code == codes.Error { // mirror the filter's OTTL condition
		drop[key] = struct{}{}
	} else {
		keep[key] = struct{}{}
	}
}
if err := pipelinetest.CheckFilterCorrectness(keep, drop, sink.WaitSettled(time.Second, 30*time.Second)); err != nil {
	t.Fatal(err)
}
```

One detail matters when adapting this: a filter or router with no default is
lossy, so there is no exact span count to wait for — use `Sink.WaitSettled`
(the bounded "eventually received" assertion), not `Sink.WaitFor`. When the
expected per-backend counts *are* known (a lossless router), `WaitFor` the
counts first and settle briefly afterwards so a misrouted straggler cannot
arrive after the assertion.

## Further reading

- [Collector pipeline testing design](../research/collector-pipeline-testing.md)
  -- the harness architecture these checks build on
- [Test sampling trace integrity](test-sampling-integrity.md) -- the sampling
  invariants over the same harness, with the full generation boilerplate
- [Test OTTL transforms](test-ottl-transforms.md) -- manually exploring what an
  OTTL statement matches, useful for authoring filter conditions
- [Property testing in motel](../explanation/property-testing.md) -- rationale
  and patterns for the rapid-based test suite
