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
./build/motel-synth run --stdout --duration 200ms examples/synth/traffic-patterns.yaml 2>/dev/null | python3 -c "
import json, sys
spans = [json.loads(line) for line in sys.stdin]
services = set()
for s in spans:
    for a in s['Attributes']:
        if a['Key'] == 'synth.service':
            services.add(a['Value']['Value'])
print('spans generated:', len(spans) > 0)
print('services:', sorted(services))
"
```

```output
spans generated: True
services: ['api', 'database']
```

## Span structure

Each span carries standard OTel fields: trace and span IDs, timestamps, attributes, and status. Root operations are `SERVER` spans (SpanKind 2); downstream calls are `CLIENT` spans (SpanKind 3).

```bash
./build/motel-synth run --stdout --duration 200ms examples/synth/traffic-patterns.yaml 2>/dev/null | python3 -c "
import json, sys
spans = [json.loads(line) for line in sys.stdin]
# Group by trace
traces = {}
for s in spans:
    tid = s['SpanContext']['TraceID']
    traces.setdefault(tid, []).append(s)
# Pick first trace
first = list(traces.values())[0]
print('spans per trace:', len(first))
kinds = sorted(set(s['SpanKind'] for s in first))
print('span kinds (2=SERVER, 3=CLIENT):', kinds)
# Check parent-child: the root has a zero parent trace ID
root = [s for s in first if s['Parent']['TraceID'] == '00000000000000000000000000000000']
print('has root span:', len(root) == 1)
print('root operation:', root[0]['Name'])
"
```

```output
spans per trace: 2
span kinds (2=SERVER, 3=CLIENT): [2, 3]
has root span: True
root operation: request
```

## Run statistics

At the end of each run, motel-synth emits a JSON summary to stderr with trace and span counts, timing, and error rates.

```bash
./build/motel-synth run --stdout --duration 500ms examples/synth/traffic-patterns.yaml 2>&1 >/dev/null | tail -1 | python3 -c "
import json, sys
stats = json.loads(sys.stdin.readline())
print('fields:', sorted(stats.keys()))
print('traces > 0:', stats['traces'] > 0)
print('spans per trace:', stats['spans'] // max(stats['traces'], 1))
"
```

```output
fields: ['elapsed_ms', 'error_rate', 'errors', 'spans', 'spans_per_second', 'traces', 'traces_per_second']
traces > 0: True
spans per trace: 2
```

Each trace in this topology produces exactly 2 spans (api.request calls database.query), so the spans-per-trace ratio is always 2.
