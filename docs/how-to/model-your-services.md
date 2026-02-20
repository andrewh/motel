# Model Your Services as a Topology

This guide covers two approaches to creating a topology that matches your real system: writing one by hand, or importing from existing trace data. Choose whichever fits your situation — or combine both.

## Option A: Write a topology by hand

Start with what you know about your system's call graph: which services exist, what operations they expose, and who calls whom.

### 1. Sketch the call graph

Map your services and their dependencies. For example, a typical web application:

```
web-gateway
  └─ GET /products → product-service.list
                       └─ database.query
  └─ POST /orders → order-service.create
                       ├─ database.insert
                       └─ payment-service.charge
```

Each arrow is a call. Each box is a service with one or more operations.

### 2. Translate to YAML

Turn the sketch into a topology file. Start minimal — you can add detail later:

```yaml
version: 1

services:
  web-gateway:
    operations:
      GET /products:
        duration: 50ms +/- 15ms
        calls:
          - product-service.list

      POST /orders:
        duration: 80ms +/- 20ms
        error_rate: 2%
        calls:
          - order-service.create

  product-service:
    operations:
      list:
        duration: 20ms +/- 5ms
        calls:
          - database.query

  order-service:
    operations:
      create:
        duration: 40ms +/- 10ms
        error_rate: 1%
        calls:
          - database.insert
          - payment-service.charge

  database:
    operations:
      query:
        duration: 5ms +/- 2ms
      insert:
        duration: 8ms +/- 3ms
        error_rate: 0.1%

  payment-service:
    operations:
      charge:
        duration: 200ms +/- 50ms
        error_rate: 3%

traffic:
  rate: 50/s
```

### 3. Validate and iterate

```sh
motel validate my-topology.yaml
```

Generate a short burst and inspect the output:

```sh
motel run --stdout --duration 2s my-topology.yaml | \
  jq -r .Name | sort | uniq -c | sort -rn
```

Check that the operation names and relative counts look right. Adjust durations and error rates until the traces look realistic.

### Tips for hand-written topologies

- **Start small.** Get two or three services working, then add more. Validate after each change.
- **Use real latency numbers.** Check your existing dashboards for p50 and standard deviation. `30ms +/- 10ms` is better than guessing `30ms`.
- **Error rates are per-span.** A 1% error rate means roughly 1 in 100 spans will be marked as errors. Child errors cascade upward, so effective error rates are higher at the root.
- **Calls are parallel by default.** If your service makes sequential downstream calls, set `call_style: sequential` on the operation.

## Option B: Import from existing traces

If you already have trace data — from a staging environment, production sampling, or a test run — you can infer a topology automatically.

### 1. Collect trace data

You need spans in one of two formats:

- **stdouttrace** — one JSON span per line, as produced by `motel run --stdout` or the OpenTelemetry Go SDK's stdout exporter
- **OTLP JSON** — the standard OTLP export format with `resourceSpans` arrays

Export spans from your collector, or capture them directly:

```sh
# Generate sample data
motel run --stdout --duration 30s topology.yaml > traces.jsonl

# Or use traces from another source
cat exported-traces.json
```

More traces produce better statistical accuracy. The import command warns if you have fewer traces than `--min-traces` (default: 1).

### 2. Run import

```sh
# From a file
motel import traces.jsonl

# From stdin (e.g. piped from another tool)
cat traces.jsonl | motel import
```

The output is a YAML topology written to stdout. Redirect it to a file:

```sh
motel import traces.jsonl > inferred-topology.yaml
```

The generated YAML includes a comment header noting how many traces and spans were analysed.

### 3. Review and adjust

The inferred topology is a starting point, not a finished product. Review it for:

- **Duration distributions** — import calculates mean and standard deviation from the observed spans. Check that these match your expectations.
- **Error rates** — derived from the proportion of error spans. Small sample sizes produce noisy estimates.
- **Call style** — import votes on parallel vs sequential based on child span timing overlap. Verify this matches your service's actual behaviour.
- **Missing services** — import can only infer what it sees. If some call paths are rare, they may not appear in a small sample.

Validate the inferred topology and generate traces from it:

```sh
motel validate inferred-topology.yaml
motel run --stdout --duration 5s inferred-topology.yaml | head -20
```

## Combining both approaches

A practical workflow is to import a rough topology from traces, then refine it by hand:

1. Collect traces from your real system
2. Run `motel import` to get a baseline topology
3. Edit the YAML to fix any inaccuracies, add missing services, or adjust distributions
4. Add scenarios for failure modes you want to simulate (latency spikes, error injection)
5. Validate and iterate

## Further reading

- [CLI reference](../reference/synth.md) — CLI flags and output format
- [DSL reference](../../cmd/motel/README.md) — full topology schema
- [Worked example: import pipeline](../explanation/worked-example/README.md) — deep-dive into how import infers topology from traces
