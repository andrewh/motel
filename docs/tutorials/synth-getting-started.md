# Getting Started with motel-synth

This tutorial walks you through generating realistic distributed traces from a YAML topology definition. By the end, you'll have a working topology file and understand how to validate, generate, and inspect synthetic traces.

## What you'll learn

- What motel-synth does and when to use it
- How to write a simple topology file
- How to validate a topology
- How to generate traces and read the output
- How to interpret run statistics

## Prerequisites

- Go toolchain installed
- motel-synth built: `make build-synth`

No server or external services required — motel-synth is completely standalone.

## What is motel-synth?

motel-synth generates OTLP traces that look like they came from a real distributed system. You describe your system's structure in YAML — services, operations, call patterns, latency distributions — and motel-synth produces traces matching that description.

This is useful for:

- **Testing observability pipelines** — feed realistic traces into collectors, backends, or dashboards without deploying real services
- **Load testing** — generate trace traffic at controlled rates
- **Demos and prototyping** — show what your system's traces will look like before building it

## Step 1: Write a topology

Create a file called `my-topology.yaml`:

```yaml
services:
  - name: gateway
    operations:
      - name: "GET /users"
        duration: 30ms +/- 10ms
        error_rate: 1%
        calls:
          - service: users
            operation: list

  - name: users
    operations:
      - name: list
        duration: 15ms +/- 5ms
        error_rate: 0.5%

traffic:
  rate: 10/s
```

This describes a two-service system: a `gateway` that calls a `users` service. Each trace starts at `GET /users` on the gateway, which calls `list` on the users service.

Three concepts compose the entire topology DSL:

- **Service** — a named microservice
- **Operation** — a unit of work within a service, with a duration distribution, error rate, and optional downstream calls
- **Traffic** — how often new traces are generated

## Step 2: Validate

Check the topology for errors before generating traces:

```sh
./build/motel-synth validate my-topology.yaml
```

You should see:

```
Configuration valid: 2 services, 1 root operations
```

If something is wrong, the error message includes the service name, operation name, and field:

```
Error: service "gateway" operation "GET /users": invalid duration: ...
```

## Step 3: Generate traces

Generate traces for 2 seconds, printing them to stdout:

```sh
./build/motel-synth run --stdout --duration 2s my-topology.yaml
```

Each line of stdout is one span in JSON format. A two-service trace produces two spans: one for the gateway, one for the users service. The gateway span is the parent; the users span is a child linked by trace ID and parent span ID.

At the end, motel-synth prints a statistics summary to stderr:

```json
{"traces":20,"spans":40,"errors":0,"failed_traces":0,...}
```

The stats go to stderr so you can capture spans and stats separately:

```sh
# Spans to a file, stats to the terminal
./build/motel-synth run --stdout --duration 2s my-topology.yaml > spans.jsonl

# Stats to a file, spans to the terminal
./build/motel-synth run --stdout --duration 2s my-topology.yaml 2> stats.json
```

## Step 4: Inspect the output

Each span line contains standard OpenTelemetry fields:

- `Name` — the operation name (e.g. "GET /users", "list")
- `SpanContext.TraceID` — shared across all spans in a trace
- `SpanContext.SpanID` — unique to this span
- `Parent.SpanID` — links child spans to their parent
- `StartTime` / `EndTime` — timestamps derived from the duration distribution
- `Status.Code` — "Unset" for success, "Error" for failures (based on error rate)

You can pipe the output through `jq` to explore:

```sh
# Show operation names and their durations
./build/motel-synth run --stdout --duration 1s my-topology.yaml | \
  jq -r '[.Name, (.EndTime | split(".")[0])] | @tsv'

# Count spans per operation
./build/motel-synth run --stdout --duration 2s my-topology.yaml | \
  jq -r .Name | sort | uniq -c | sort -rn
```

## Step 5: Send traces to a collector

To send traces to an OTLP-compatible collector instead of stdout, use `--endpoint`:

```sh
./build/motel-synth run --endpoint localhost:4318 --duration 10s my-topology.yaml
```

This sends traces over HTTP to the OTLP endpoint. The collector receives them as if they came from real instrumented services.

## Next steps

- **[Example topologies](../../examples/synth/)** — ready-to-use YAML files covering error cascading, traffic patterns, scenarios, and more
- **[motel-synth reference](../../cmd/motel-synth/README.md)** — full DSL reference for all topology fields
- **[Worked example: importing from traces](../explanation/synth/worked-example/README.md)** — reverse the workflow by inferring a topology from real trace data
- **[Showboat demos](../../demos/INDEX.md)** (demos 17–24) — executable walkthroughs of every feature
