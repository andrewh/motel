# motel-synth: Getting Started

*2026-02-11T16:00:00Z*

motel-synth generates synthetic OTLP traces from a YAML topology definition. This demo walks through the basics: checking the tool, validating a config, generating traces, and inspecting the output.

## Version check

Confirm motel-synth is built and available.

```bash
./build/motel-synth version | grep -c 'motel-synth'
```

```output
1
```

## The topology file

A topology defines services, their operations, call relationships, and traffic settings. Here is a minimal two-service example.

```bash
cat examples/synth/traffic-patterns.yaml
```

```output
# Minimal topology for demonstrating traffic patterns
# Two services, one call — keeps output easy to read

version: 1
services:
  api:
    operations:
      request:
        duration: 10ms +/- 3ms
        error_rate: 1%
        calls:
          - database.query

  database:
    operations:
      query:
        duration: 5ms +/- 2ms
        error_rate: 0.1%

traffic:
  rate: 50/s
  pattern: uniform
```

## Validation

The `validate` command checks structural correctness: service and operation references, duration formats, error rates, and traffic configuration.

```bash
./build/motel-synth validate examples/synth/traffic-patterns.yaml
```

```output
Configuration valid: 2 services, 1 root operations
```

The validator catches common mistakes. For example, a broken call reference:

```bash
cat > /tmp/bad-topology.yaml << 'EOF'
version: 1
services:
  api:
    operations:
      request:
        duration: 10ms
        calls:
          - nonexistent.op
traffic:
  rate: 10/s
EOF
./build/motel-synth validate /tmp/bad-topology.yaml 2>&1 | head -1
```

```output
Error: service "api" operation "request": call "nonexistent.op" references unknown operation
```

## Generating traces

Run with `--stdout` to emit spans as JSON, one per line. The `--duration` flag controls how long the generator runs.

```bash
./build/motel-synth run --stdout --duration 200ms examples/synth/traffic-patterns.yaml 2>/dev/null | jq -rs '
  "spans generated: \(length > 0)",
  "services: \([.[].Attributes[] | select(.Key == "synth.service") | .Value.Value] | unique)"'
```

```output
spans generated: true
services: ["api","database"]
```

## Span structure

Each span carries standard OTel fields: trace and span IDs, timestamps, attributes, and status. Root operations are `SERVER` spans (SpanKind 2); downstream calls are `CLIENT` spans (SpanKind 3).

```bash
./build/motel-synth run --stdout --duration 200ms examples/synth/traffic-patterns.yaml 2>/dev/null | jq -rs '
  group_by(.SpanContext.TraceID) | .[0] |
  "spans per trace: \(length)",
  "span kinds (2=SERVER, 3=CLIENT): \([.[].SpanKind] | unique | sort)",
  "has root span: \(map(select(.Parent.TraceID == "00000000000000000000000000000000")) | length == 1)",
  "root operation: \(map(select(.Parent.TraceID == "00000000000000000000000000000000")) | .[0].Name)"'
```

```output
spans per trace: 2
span kinds (2=SERVER, 3=CLIENT): [2,3]
has root span: true
root operation: request
```

## Run statistics

At the end of each run, motel-synth emits a JSON summary to stderr with trace and span counts, timing, and error rates.

```bash
./build/motel-synth run --stdout --duration 500ms examples/synth/traffic-patterns.yaml 2>&1 >/dev/null | tail -1 | jq -r '
  "fields: \(keys)",
  "traces > 0: \(.traces > 0)",
  "spans per trace: \(.spans / (if .traces == 0 then 1 else .traces end) | floor)"'
```

```output
fields: ["elapsed_ms","error_rate","errors","spans","spans_per_second","traces","traces_per_second"]
traces > 0: true
spans per trace: 2
```

Each trace in this topology produces exactly 2 spans (api.request calls database.query), so the spans-per-trace ratio is always 2.
