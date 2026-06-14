# motel Reference

Standalone CLI that generates realistic distributed traces from a YAML topology definition.

## Topology source

All commands that accept a topology (`validate`, `run`, `check`, `preview`) accept either a local file path or an HTTP/HTTPS URL:

```sh
motel validate topology.yaml
motel validate https://example.com/topology.yaml
```

URL fetches have a 10-second timeout and a 10 MB response body limit. Redirects are followed up to 3 hops.

## Commands

### validate

Check a topology for errors without generating any output.

```sh
motel validate <topology.yaml | URL> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--semconv` | string | | Directory of additional semantic convention YAML files |

Prints a summary on success (e.g. `Configuration valid: 5 services, 2 root operations`) or a precise error on failure including the service name, operation name, and field.

When a metric name matches a known OpenTelemetry semantic convention metric, validate also checks the instrument type and unit against the convention. Mismatches are reported as warnings on stderr, not errors — users may intentionally deviate, and custom metric names are never warned about:

```
warning: service "gateway": metric "http.server.request.duration": unit "ms" does not match semantic convention unit "s"
```

Log attribute names are also checked against the semantic conventions. Validate warns when an attribute is deprecated, or when a static value is not a member of a known enum. Unknown attribute names are never warned about — custom attributes are allowed:

```
warning: service "gateway" log[0]: attribute "log.iostream": value syslog is not a member of the semantic convention enum
```

### run

Generate synthetic signals from a topology definition.

```sh
motel run <topology.yaml | URL> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--duration` | duration | `1m` | Simulation duration |
| `--endpoint` | string | | OTLP endpoint (e.g. `localhost:4318` or `http://localhost:4318`) |
| `--protocol` | string | `http/protobuf` | OTLP protocol (`http/protobuf` or `grpc`) |
| `--signals` | string | `traces` | Comma-separated signals: `traces`, `metrics`, `logs` |
| `--slow-threshold` | duration | `1s` | Spans exceeding this duration emit a slow-span log record. Warns and has no effect unless `logs` is included in `--signals` |
| `--max-spans-per-trace` | int | 0 | Maximum spans per trace (safety limit for deep topologies); 0 means the default of 10000 |
| `--stdout` | bool | false | Emit signals to stdout as JSON instead of sending to an endpoint |
| `--semconv` | string | | Directory of additional semantic convention YAML files |
| `--label-scenarios` | bool | false | Add a `synth.scenarios` attribute to spans listing active scenario names |
| `--time-offset` | duration | 0 | Shift span timestamps by this duration (e.g. `-1h` for past, `1h` for future) |
| `--realtime` | bool | false | Emit spans at wall-clock times matching simulated timestamps |
| `--seed` | uint | 0 | Seed for deterministic simulation decisions (0 = random); determinism is best-effort and not guaranteed across motel versions |
| `--pprof` | string | | Start a pprof HTTP server on this address (e.g. `:6060`) |

`--realtime` and `--time-offset` are mutually exclusive.

#### Output format

When `--stdout` is used, motel writes to two streams:

- **stdout** — emitted signal records as JSON. Trace-only output uses stdouttrace format from the OpenTelemetry Go SDK: one span JSON object per line. Metrics and logs use their own OpenTelemetry stdout exporter JSON shapes.
- **stderr** — a single JSON statistics object on the final line, containing `traces`, `spans`, `errors`, `failed_traces`, `error_rate`, and other run metrics.

To capture them separately:

```sh
# Trace-only stdouttrace spans to file, stats to terminal
motel run --stdout --duration 5s topology.yaml > spans.jsonl

# Stats to file, spans to terminal
motel run --stdout --duration 5s topology.yaml 2> stats.json

# Both to separate files
motel run --stdout --duration 5s topology.yaml > spans.jsonl 2> stats.json
```

Trace-only stdout uses the same format accepted by `motel import`, so you can round-trip: generate traces, then infer a topology from them.
This import round-trip applies to trace-only stdout output. When `--signals`
includes metrics or logs, stdout may include metric export payloads or log
records with different JSON shapes; mixed-signal stdout is useful for
inspection and debugging, but should not be piped directly to `motel import`.

### emit

Emit one or more single-span traces from command-line arguments, without a topology file. For multi-service topologies or call graphs, use `motel run`.

```sh
motel emit --service <name> --operation <name> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--service` | string | | Service name (required) |
| `--operation` | string | | Operation name (required) |
| `--span-duration` | duration | `100ms` | Span duration |
| `--duration` | duration | | Simulation duration, e.g. `10s`, `5m` |
| `--error-rate` | string | | Error rate (e.g. `5%`, `0.05`) |
| `--attr` | string | | Span attribute in `key=value` format (repeatable) |
| `--count` | int | 1 | Number of traces to emit |
| `--rate` | string | `10/s` | Trace rate when count > 1 (e.g. `10/s`, `100/m`) |
| `--endpoint` | string | | OTLP endpoint (e.g. `localhost:4318` or `http://localhost:4318`) |
| `--protocol` | string | `http/protobuf` | OTLP protocol (`http/protobuf` or `grpc`) |
| `--stdout` | bool | false | Emit signals to stdout as JSON |

### check

Run structural checks on a topology before sending traffic.

```sh
motel check <topology.yaml | URL> [flags]
```

Computes worst-case trace depth, fan-out per span, and total spans per trace using static graph analysis. Optionally runs sampled exploration with the engine to measure empirical values. Exits with code 0 if all checks pass, 1 if any check fails.

Scenarios defined in the topology are explored automatically. Scenario windows are swept to find every distinct combination of co-active scenarios, and each combination — plus the baseline with no scenarios — is checked with its overrides applied (including `add_calls` and `remove_calls`). Each check reports the worst case found and which scenario combination produced it, so a topology that passes in isolation but explodes when a scenario adds calls fails the check.

Sampling uses the `random` strategy by default, which follows the topology's configured probabilities. Use `--sample-strategy swarm` to partition probabilistic call, operation error, and retry choices across sample runs for corner-case exploration. Swarm samples are useful for finding structural bounds that pure random sampling may miss, but their percentile summaries are not empirical production-frequency percentiles. See [Swarm Testing for Topology Exploration](../explanation/swarm-testing.md).

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--max-depth` | int | 10 | Fail if worst-case trace depth exceeds this |
| `--max-fan-out` | int | 100 | Fail if worst-case children per span exceeds this |
| `--max-spans` | int | 10000 | Fail if worst-case spans per trace exceeds this |
| `--samples` | int | 1000 | Sampled traces for empirical measurement per scenario combination (0 to skip) |
| `--seed` | uint | 0 | Random seed for reproducibility (0 = random) |
| `--max-spans-per-trace` | int | 0 | Maximum spans per sampled trace; 0 means the default of 10000 |
| `--semconv` | string | | Directory of additional semantic convention YAML files |
| `--checks` | string | | YAML checks file or URL with structural thresholds |
| `--sample-strategy` | string | `random` | Sample strategy: `random` or `swarm` |
| `--skip-scenarios` | bool | false | Check the baseline topology only, ignoring scenarios |

Output is one line per check showing PASS/FAIL, the measured value, and the limit. Depth checks include the worst-case path; fan-out checks identify the worst operation; span checks show both static worst-case and observed values from sampling. When a scenario combination produces the worst case, the check is annotated with `scenarios:` naming it.

Use `--checks` to load project-specific thresholds from a separate YAML file or HTTP/HTTPS URL. Explicit command-line limit flags override matching values from the checks source.

```yaml
version: 1
checks:
  max_depth: 8
  max_fan_out: 100
  max_spans: 200
  p95_depth: 6
  p99_spans: 150
```

Static thresholds (`max_depth`, `max_fan_out`, `max_spans`) reuse the same checks as the matching flags. Percentile thresholds (`p50_*`, `p95_*`, `p99_*` for `depth`, `fan_out`, and `spans`) are evaluated from sampled traces, so they require sampling to be enabled.

### import

Infer a topology from existing trace data.

```sh
motel import [file] [flags]
```

Reads trace spans and generates a YAML topology. If no file is given, reads from stdin.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--format` | string | `auto` | Input format: `auto`, `stdouttrace`, `otlp`, or `jaeger` |
| `--min-traces` | int | 1 | Minimum traces for statistical accuracy (warns if fewer) |

The `auto` format detector examines the JSON structure to determine whether the input is stdouttrace JSON (one span per line), OTLP JSON (batched export format), or Jaeger JSON such as Grafana Explore Tempo downloads.

Output is written to stdout as a YAML topology with a commented header noting how many traces and spans were analysed.
When `--min-traces` is greater than 1, confidence diagnostics are written to stderr when inferred operations, downstream call probabilities, or call-style votes are based on weak evidence relative to that sample target. Redirecting stdout still produces valid YAML suitable for `motel validate`.

### preview

Render the traffic rate over time as an SVG chart.

```sh
motel preview <topology.yaml | URL> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--duration` | duration | inferred from topology | Preview duration |
| `--output`, `-o` | string | stdout | Output file path |

### version

Print the motel version, commit, and build time.

```sh
motel version
```

## Topology DSL

The full DSL reference — services, operations, calls, attributes, traffic, and scenarios — is documented in the [motel README](../../cmd/motel/README.md).

## Further reading

- [Getting started tutorial](../tutorials/getting-started.md)
- [Example topologies](../examples/README.md)
- [How import infers a topology](../explanation/import-pipeline/README.md)
- [Modelling your services](../how-to/model-your-services.md)
