# motel

Standalone CLI that generates realistic distributed traces from a YAML topology
definition. No server, no live services — describe the behaviour of your
system, and motel derives telemetry from it.

## Mental Model

Three nouns compose the entire DSL:

- **Service** — a named microservice with static resource attributes
- **Operation** — a unit of work within a service: duration distribution, error
  rate, downstream calls, and per-span attributes
- **Scenario** — a time-windowed override that mutates operations during a
  defined interval (latency spikes, error injection, traffic changes)

Services contain operations. Operations call other operations. Scenarios mutate
operations during time windows. The engine walks the topology tree once per
trace, sampling durations and errors from the configured distributions.

## Quick Start

```sh
go install github.com/andrewh/motel/cmd/motel@latest

# Validate a topology file
motel validate docs/examples/basic-topology.yaml

# Generate traces to stdout for 5 seconds
motel run --stdout --duration 5s docs/examples/basic-topology.yaml
```

## DSL Reference

### version

Required. Must be `1`. This field identifies the topology schema version so
that future changes to the DSL can be handled without breaking existing files.

```yaml
version: 1
```

### services

Map of service name to definition. Each service has optional `attributes`
(static string key-value resource attributes) and a required `operations` map.

```yaml
services:
  gateway:
    attributes:
      deployment.environment: production
      service.namespace: demo
    operations:
      GET /users:
        # ...
```

### operations

Each operation defines the span it produces.

| Field        | Type   | Description |
|-------------|--------|-------------|
| `duration`   | string | Mean with optional stddev: `30ms +/- 10ms` or fixed `50ms` |
| `error_rate` | string | Percentage `0.5%` or decimal `0.005` |
| `call_style` | string | `parallel` or `sequential` (default: parallel) |
| `domain`     | string | Semconv shorthand (e.g. `http`) — auto-generates standard attributes |
| `attributes` | map    | Per-span attribute generators (see below) |
| `calls`      | list   | Downstream calls to other operations |

```yaml
operations:
  create:
    duration: 50ms +/- 15ms
    error_rate: 0.5%
    call_style: parallel
    attributes:
      http.request.method:
        value: POST
    calls:
      - postgres.query
      - redis.get
```

### calls

Each call references a `service.operation` target. Supports a string shorthand
or a full mapping.

| Field          | Type   | Description |
|---------------|--------|-------------|
| `target`       | string | `service.operation` reference |
| `probability`  | float  | Chance of executing (0-1, default: always) |
| `condition`    | string | `on-error` or `on-success` — only fire based on caller's own error state |
| `count`        | int    | Number of times to repeat the call |
| `timeout`      | string | Cap child span duration (Go duration, e.g. `100ms`) |
| `retries`      | int    | Retry count on child failure |
| `retry_backoff`| string | Constant delay between retries (Go duration) |

```yaml
calls:
  # String shorthand
  - database.query
  # Full mapping
  - target: cache.lookup
    probability: 0.8
    timeout: 50ms
    retries: 2
    retry_backoff: 10ms
```

### duration format

`mean +/- stddev` using Go duration units (`ns`, `us`/`µs`, `ms`, `s`, `m`,
`h`). The `±` unicode variant is also accepted. Omitting `+/-` gives a fixed
duration with zero variance. Sampled from a normal distribution, clamped to
zero.

### attribute generators

Exactly one field must be set per attribute. Each generator produces a typed
span attribute value.

**`value`** — static value

```yaml
http.request.method:
  value: GET
```

**`values`** — weighted random choice (keys are values, ints are relative weights)

```yaml
http.response.status_code:
  values: { "200": 95, "404": 3, "500": 2 }
```

**`sequence`** — incrementing pattern (`{n}` replaced with counter)

```yaml
user.id:
  sequence: "user-{n}"
```

**`probability`** — boolean with threshold (0-1)

```yaml
feature.enabled:
  probability: 0.3
```

**`range`** — uniform random integer `[min, max]`

```yaml
http.response.body.size:
  range: [100, 5000]
```

**`distribution`** — normally distributed float (`mean`, `stddev`)

```yaml
response.latency:
  distribution: { mean: 50.0, stddev: 10.0 }
```

### traffic

Controls trace arrival rate.

| Field             | Type   | Description |
|------------------|--------|-------------|
| `rate`            | string | Base rate, e.g. `50/s`, `3000/m` |
| `pattern`         | string | `uniform` (default), `diurnal`, `bursty`, `custom` |
| `burst_multiplier`| float  | Rate multiplier during bursts (bursty only, default: 5) |
| `burst_interval`  | string | Time between burst starts (bursty only, default: 5m) |
| `burst_duration`  | string | Length of each burst (bursty only, default: 30s) |
| `peak_multiplier` | float  | Peak of sine wave (diurnal only, default: 1.5) |
| `trough_multiplier`| float | Trough of sine wave (diurnal only, default: 0.5) |
| `period`          | string | Cycle length (diurnal only, default: 24h) |
| `segments`        | list   | Time-bounded rate segments (custom only) |
| `overlay`         | object | Nested traffic config layered on top of the base pattern |

```yaml
traffic:
  rate: 100/s
  pattern: bursty
  burst_multiplier: 10
  burst_interval: 2m
  burst_duration: 15s
```

### scenarios

Time-windowed overrides to operation behaviour and traffic.

| Field      | Type   | Description |
|-----------|--------|-------------|
| `name`     | string | Human-readable label |
| `at`       | string | Start offset from simulation start, e.g. `+5s`, `30s` |
| `duration` | string | How long the scenario is active |
| `priority` | int    | Higher priority wins when scenarios overlap (default: 0) |
| `override` | map    | Per-operation overrides keyed by `service.operation` |
| `traffic`  | object | Traffic pattern override for this window |

Each override can set `duration`, `error_rate`, and `attributes`. Overlapping
scenarios merge overrides by priority — higher-priority values win per field,
attributes merge per key.

```yaml
scenarios:
  - name: database degradation
    at: +5s
    duration: 10s
    override:
      postgres.query:
        duration: 500ms +/- 100ms
        error_rate: 15%
    traffic:
      rate: 200/s
```

## Derived Signals

By default motel emits traces only. Use `--signals` to add metrics and
logs derived from the same trace data:

```sh
motel run --stdout --signals traces,metrics,logs \
  --slow-threshold 200ms docs/examples/basic-topology.yaml
```

`--slow-threshold` controls which spans generate log records (spans exceeding
the threshold emit a slow-span log). All three signal types are driven by the
same topology — no separate configuration needed.

## Design Decisions

**Synthetic timestamps.** The engine does not sleep per span. Wall-clock time
is only used for rate control between traces. This means you can generate hours
of simulated traffic in seconds.

**Cascading failure.** Per-call `timeout` caps child span duration. `retries`
re-executes the child call with constant `retry_backoff` delay. Child errors
cascade upward — a failing child marks its parent span as errored. The
`on-error` and `on-success` conditions evaluate the caller's own error rate,
not the child's outcome.

**Standalone.** motel has no dependency on any server. It outputs
OTLP to any collector via `--endpoint`, or JSON to stdout with `--stdout`.
Protocol is configurable with `--protocol` (http/protobuf or grpc).

**YAML DSL.** The topology is a human-readable YAML file, validated before
execution. Enough structure to catch mistakes early, loose enough to experiment
quickly.

## Further Reading

- [`docs/examples/`](../../docs/examples/) — example topology configs
- [`docs/tutorials/`](../../docs/tutorials/) — getting started tutorial
- [`pkg/synth/`](../../pkg/synth/) — implementation source
