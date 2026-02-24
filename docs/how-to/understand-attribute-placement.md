# Understand Attribute Placement and Cardinality

This guide covers how motel models resource attributes and span attributes, how to experiment with moving attributes between levels, and how to use attribute generators to explore cardinality impact before deploying changes to production.

## Prerequisites

- motel installed
- A topology file (see [Model your services](model-your-services.md))
- A tracing backend or the `--stdout` flag for local inspection

## Resource attributes vs span attributes

motel distinguishes two levels of attributes, matching the OpenTelemetry data model:

- **Resource attributes** are defined under `services.<name>.attributes`. They describe the service itself and are attached to every span the service produces. These are static string key-value pairs.
- **Span attributes** are defined under `services.<name>.operations.<op>.attributes`. They describe individual operations and can vary per span using attribute generators.

```yaml
services:
  gateway:
    attributes:                        # resource attributes
      deployment.environment: production
      service.namespace: demo
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        attributes:                    # span attributes
          http.request.method:
            value: GET
          http.response.status_code:
            values: {200: 95, 404: 3, 500: 2}
```

Resource attributes appear once per service resource in the exported telemetry. Span attributes appear on each individual span. This distinction matters for storage cost, query performance, and how your backend indexes data.

## Experiment: move an attribute between levels

A practical way to understand the difference is to move an attribute from one level to the other and observe the result.

### Start with a resource attribute

Create a file called `placement-test.yaml`:

```yaml
version: 1

services:
  api:
    attributes:
      deployment.environment: staging
    operations:
      handle:
        duration: 10ms

traffic:
  rate: 10/s
```

Generate traces and inspect the output:

```sh
motel run --stdout --duration 3s placement-test.yaml | head -20
```

Notice that `deployment.environment` appears on every span from the `api` service with the same value. Because it is defined at the service level, it is automatically attached to all operations — you do not need to repeat it on each operation.

### Move it to a span attribute

Now move `deployment.environment` from the service level to the operation level:

```yaml
version: 1

services:
  api:
    operations:
      handle:
        duration: 10ms
        attributes:
          deployment.environment:
            value: staging

traffic:
  rate: 10/s
```

Run the same command:

```sh
motel run --stdout --duration 3s placement-test.yaml | head -20
```

The `--stdout` output looks similar in both cases — motel attaches all attributes to each span. The difference matters when the telemetry reaches a real backend. In the OpenTelemetry data model, service-level attributes belong on the Resource and are stored once per service, while span-level attributes are stored on every individual span. Placing a constant value at the span level increases storage cost and may change how you query the attribute.

To see this distinction in practice, send the two versions to a collector and compare how your backend indexes them:

```sh
motel run --endpoint localhost:4318 --duration 10s placement-test.yaml
```

**Rule of thumb:** attributes that are constant for a service belong at the service level. Attributes that vary per request belong at the operation level.

## Attribute generators and cardinality

Span attributes in motel use generators that control how many distinct values an attribute produces. This directly maps to cardinality — the number of unique values a backend must index.

### Low cardinality: `value` and `values`

A `value` generator always produces the same string — cardinality of 1:

```yaml
http.request.method:
  value: GET
```

A `values` generator picks from a fixed set with weighted probability — cardinality equals the number of choices:

```yaml
http.response.status_code:
  values:
    200: 95
    404: 3
    500: 2
```

These are safe for most backends. The set of distinct values is small and bounded.

### High cardinality: `sequence`

A `sequence` generator produces a unique value for every span:

```yaml
user.id:
  sequence: "user-{n}"
```

This creates `user-1`, `user-2`, `user-3`, and so on — unbounded cardinality. Add this to a topology and send traffic to your backend to see how it handles high-cardinality attributes.

### Numeric range: `range`

A `range` generator produces random integers within bounds:

```yaml
http.response.content_length:
  range: [0, 50000]
```

Cardinality is bounded by the range size but can still be high. A range of `[0, 50000]` produces up to 50,001 distinct values.

### Controlled distribution: `distribution`

A `distribution` generator samples from a normal distribution:

```yaml
queue.depth:
  distribution:
    mean: 100
    stddev: 20
```

Values cluster around the mean but the theoretical range is unbounded.

### Boolean: `probability`

A `probability` generator produces true/false with the given probability — cardinality of 2:

```yaml
cache.hit:
  probability: 0.8
```

## Test cardinality impact on your backend

Combine these generators in a topology to simulate realistic and adversarial attribute patterns:

```yaml
version: 1

services:
  api:
    attributes:
      deployment.environment: staging
    operations:
      handle:
        duration: 15ms +/- 5ms
        attributes:
          http.request.method:
            values: {"GET": 70, "POST": 20, "PUT": 10}
          user.id:
            sequence: "user-{n}"
          http.response.status_code:
            values: {200: 90, 400: 5, 500: 5}
          response.size:
            range: [100, 10000]

traffic:
  rate: 50/s
```

Send this to your backend and monitor:

```sh
motel run --endpoint localhost:4318 --duration 60s cardinality-test.yaml
```

Watch for:

- **Index growth** — high-cardinality attributes like `user.id` cause index bloat in most tracing backends
- **Query performance** — try querying by `user.id` vs `http.request.method` and compare response times
- **Storage cost** — compare the data volume with and without the `user.id` attribute

To isolate the effect of a single attribute, run the topology twice — once with the high-cardinality attribute and once without — and compare the results in your backend.

## Semantic conventions and correct placement

The `--semconv` flag helps ensure attributes are placed correctly according to OpenTelemetry semantic conventions. It validates that attribute names and placements match convention definitions.

```sh
motel validate --semconv /path/to/semconv topology.yaml
```

The `--semconv` flag points to a directory containing semantic convention YAML files. motel loads the embedded OpenTelemetry conventions by default and merges any additional conventions you provide.

This catches common mistakes:

- Using a deprecated attribute name when a replacement exists
- Placing an attribute at the wrong level (resource vs span)
- Misspelling a well-known attribute name

You can also use `--semconv` with `motel run` to validate at generation time:

```sh
motel run --stdout --semconv /path/to/semconv --duration 5s topology.yaml
```

## Further reading

- [Model your services](model-your-services.md) — creating topology files from scratch or from traces
- [CLI reference](../reference/synth.md) — full list of flags and options
- [Basic topology example](../examples/basic-topology.yaml) — a complete topology with resource and span attributes
