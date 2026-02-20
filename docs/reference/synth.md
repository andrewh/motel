# motel Reference

Standalone CLI that generates realistic distributed traces from a YAML topology definition.

## Commands

### validate

Check a topology file for errors without generating any output.

```sh
motel validate <config.yaml>
```

Prints a summary on success (e.g. `Configuration valid: 5 services, 2 root operations`) or a precise error on failure including the service name, operation name, and field.

### run

Generate synthetic signals from a topology definition.

```sh
motel run <config.yaml> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--duration` | duration | from config, or 1m | Simulation duration |
| `--endpoint` | string | | OTLP endpoint (e.g. `localhost:4318`) |
| `--protocol` | string | `http/protobuf` | OTLP protocol (`http/protobuf` or `grpc`) |
| `--signals` | string | `traces` | Comma-separated signals: `traces`, `metrics`, `logs` |
| `--slow-threshold` | duration | `1s` | Spans exceeding this duration emit a slow-span log record. Only has an effect when `logs` is included in `--signals` |
| `--max-spans-per-trace` | int | 10000 | Maximum spans per trace (safety limit for deep topologies) |
| `--stdout` | bool | false | Emit signals to stdout as JSON instead of sending to an endpoint |

#### Output format

When `--stdout` is used, motel writes to two streams:

- **stdout** — one JSON object per line, one span per line (stdouttrace format from the OpenTelemetry Go SDK). This is newline-delimited JSON, not the OTLP wire format.
- **stderr** — a single JSON statistics object on the final line, containing `traces`, `spans`, `errors`, `failed_traces`, `error_rate`, and other run metrics.

To capture them separately:

```sh
# Spans to file, stats to terminal
motel run --stdout --duration 5s topology.yaml > spans.jsonl

# Stats to file, spans to terminal
motel run --stdout --duration 5s topology.yaml 2> stats.json

# Both to separate files
motel run --stdout --duration 5s topology.yaml > spans.jsonl 2> stats.json
```

The stdout format is the same format accepted by `motel import`, so you can round-trip: generate traces, then infer a topology from them.

### import

Infer a topology from existing trace data.

```sh
motel import [file] [flags]
```

Reads trace spans and generates a YAML topology. If no file is given, reads from stdin.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--format` | string | `auto` | Input format: `auto`, `stdouttrace`, or `otlp` |
| `--min-traces` | int | 1 | Minimum traces for statistical accuracy (warns if fewer) |

The `auto` format detector examines the first line to determine whether the input is stdouttrace JSON (one span per line) or OTLP JSON (batched export format).

Output is written to stdout as a YAML topology with a commented header noting how many traces and spans were analysed.

## Topology DSL

The full DSL reference — services, operations, calls, attributes, traffic, and scenarios — is documented in the [motel README](../../cmd/motel/README.md).

## Further reading

- [Getting started tutorial](../tutorials/getting-started.md)
- [Example topologies](../examples/)
