# motel-synth: Importing Topology from Traces

*2026-02-15T20:54:56Z by Showboat 0.5.0*

The `motel-synth import` command reverses the normal workflow: instead of writing a topology by hand, you feed in real trace data and it infers one for you. This is useful for bootstrapping a synth topology from production traces or from another tracing tool's output.

For a detailed walkthrough of how each pipeline stage processes real trace data, see the [worked example](../docs/explanation/synth/worked-example/README.md).

## How the inference works

The import pipeline analyses trace data in stages:

1. **Parse spans** -- reads stdouttrace (line-delimited JSON) or OTLP protobuf JSON, normalising both into a common span representation
2. **Build trace trees** -- links parent and child spans by span ID, reconstructing the call graph per trace
3. **Collect statistics** -- for each (service, operation) pair: duration mean and standard deviation, error rate, and which downstream operations are called
4. **Infer call style** -- when an operation has multiple children, votes parallel (children start within 1ms of each other) or sequential (each child starts after the previous ends)
5. **Infer call probability** -- if a child call appears in only some invocations of the parent, the probability is recorded (e.g. 0.5 means called half the time)
6. **Detect service attributes** -- attributes with the same value on every span of a service are promoted to service-level attributes in the topology
7. **Compute traffic rate** -- divides trace count by the observation time window
8. **Round-trip validation** -- the generated YAML is loaded back through `synth.LoadConfig` and `synth.ValidateConfig` to guarantee it produces a valid topology

## Generate sample traces

First, generate some traces from an existing topology. We use `--stdout` to emit stdouttrace JSON (one span per line), then pipe it into `import`.

```bash
./build/motel-synth run --stdout --duration 500ms examples/synth/basic-topology.yaml 2>/dev/null | head -1 | jq -r "\"format: stdouttrace (line-delimited JSON)\", \"has SpanContext: \\(.SpanContext | length > 0)\", \"operation: \\(.Name)\""
```

```output
format: stdouttrace (line-delimited JSON)
has SpanContext: true
operation: query
```

## Import a topology

Pipe the generated traces directly into `import`. It auto-detects the input format, runs the inference pipeline, and emits a topology YAML.

```bash
./build/motel-synth run --stdout --duration 1s examples/synth/basic-topology.yaml 2>/dev/null | ./build/motel-synth import 2>/dev/null > /tmp/synth-imported.yaml && echo "version: $(grep -c "^version:" /tmp/synth-imported.yaml)" && echo "services discovered: $(grep -c "^    operations:" /tmp/synth-imported.yaml)" && echo "has calls: $(grep -c "calls:" /tmp/synth-imported.yaml | xargs test 0 -lt && echo true)" && echo "has traffic rate: $(grep -q "rate:" /tmp/synth-imported.yaml && echo true)"
```

```output
version: 1
services discovered: 5
has calls: true
has traffic rate: true
```

The import discovered all 5 services from the basic topology. Each operation gets an inferred duration (mean +/- stddev), an error rate if errors were observed, and a list of downstream calls. The output header shows how many traces and spans were analysed.

## What the inferred topology looks like

The YAML structure matches what you would write by hand: services, operations, durations, error rates, calls, and traffic rate.

```bash
./build/motel-synth run --stdout --duration 1s examples/synth/basic-topology.yaml 2>/dev/null | ./build/motel-synth import 2>/dev/null > /tmp/synth-imported.yaml && echo "--- services (sorted) ---" && grep "^  [a-z]" /tmp/synth-imported.yaml | grep -v "rate:" | sed "s/:$//" | sort && echo "--- inferred fields ---" && echo "durations: $(grep -c "duration:" /tmp/synth-imported.yaml)" && echo "call relationships: $(grep -c "calls:" /tmp/synth-imported.yaml)" && echo "service attributes: $(grep -c "deployment.environment" /tmp/synth-imported.yaml)"
```

```output
--- services (sorted) ---
  gateway
  order-service
  postgres
  redis
  user-service
--- inferred fields ---
durations: 6
call relationships: 4
service attributes: 3
```

The topology has 6 operations across 5 services, 4 call relationships linking them, and 3 services with constant `deployment.environment` attributes detected from the span data. Error rates would also appear if errors were observed in the input traces.

## Round-trip validation

Every imported topology is automatically validated: the YAML is loaded back through `synth.LoadConfig` and `synth.ValidateConfig` to guarantee it will work with `motel-synth run`. We can also validate explicitly.

```bash
./build/motel-synth validate /tmp/synth-imported.yaml
```

```output
Configuration valid: 5 services, 2 root operations
```

## Explicit format selection

By default, `import` auto-detects the input format by inspecting the JSON structure. You can also specify `--format stdouttrace` or `--format otlp` to skip detection.

```bash
./build/motel-synth run --stdout --duration 500ms examples/synth/basic-topology.yaml 2>/dev/null | ./build/motel-synth import --format stdouttrace 2>/dev/null | grep -c "^version:"
```

```output
1
```

## Reading from a file

The `import` command reads from stdin by default, but also accepts a file path argument.

```bash
./build/motel-synth run --stdout --duration 500ms examples/synth/basic-topology.yaml 2>/dev/null > /tmp/traces.jsonl && echo "saved spans: $(test $(wc -l < /tmp/traces.jsonl) -gt 10 && echo true)" && echo "import from file: $(./build/motel-synth import /tmp/traces.jsonl 2>/dev/null | grep -c "^version:")"
```

```output
saved spans: true
import from file: 1
```

## Minimum trace warning

The `--min-traces` flag warns when fewer traces are available than desired for statistical accuracy. With more traces, duration distributions and call probabilities become more representative.

```bash
./build/motel-synth run --stdout --duration 200ms examples/synth/basic-topology.yaml 2>/dev/null | ./build/motel-synth import --min-traces 1000 2>&1 >/dev/null | grep -c "warning"
```

```output
1
```

## Full round-trip: generate, import, re-generate

The ultimate test: generate traces from a topology, import to infer a new topology, then use the inferred topology to generate new traces.

```bash
./build/motel-synth run --stdout --duration 1s examples/synth/basic-topology.yaml 2>/dev/null | ./build/motel-synth import 2>/dev/null > /tmp/inferred.yaml && ./build/motel-synth validate /tmp/inferred.yaml && ./build/motel-synth run --stdout --duration 200ms /tmp/inferred.yaml 2>/dev/null | jq -rs "\"re-generated traces: \(map(.SpanContext.TraceID) | unique | length > 0)\", \"re-generated services: \([.[].Attributes[] | select(.Key == \"synth.service\") | .Value.Value] | unique | sort)\""
```

```output
Configuration valid: 5 services, 2 root operations
re-generated traces: true
re-generated services: ["gateway","order-service","postgres","redis","user-service"]
```
