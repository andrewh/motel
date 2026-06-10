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

Map of service name to definition. Each service has a required `operations` map
and optional resource attributes.

| Field                  | Type | Description |
|------------------------|------|-------------|
| `resource_attributes`  | map  | Static string key-value pairs attached to the OTel resource (not spans). Use for `deployment.environment`, `service.version`, `service.namespace`, etc. `service.name` and `motel.version` are set automatically and cannot be overridden |
| `attributes`           | map  | Static string key-value pairs added to every span from this service |
| `metrics`              | list | Metric instruments emitted by this service (see [metrics](#metrics)) |
| `operations`           | map  | Operation definitions (required) |

```yaml
services:
  gateway:
    resource_attributes:
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
| `error_rate` | string | Percentage `0.5%` or decimal `0.005` (0.0 to 1.0) |
| `call_style` | string | `parallel` or `sequential` (default: parallel) |
| `domain`     | string | Semconv shorthand (e.g. `http`) — auto-generates standard attributes |
| `attributes` | map    | Per-span attribute generators (see below) |
| `metrics`    | list   | Metric instruments scoped to this operation (see [metrics](#metrics)) |
| `events`     | list   | Span events emitted during the operation (see below) |
| `links`      | list   | Cross-trace span links to other operations (see below) |
| `calls`      | list   | Downstream calls to other operations |
| `queue_depth`| int    | Max concurrent requests before rejection (0 = unlimited) |
| `backpressure`| object | Latency-driven degradation: increases duration and error rate when a downstream call exceeds a threshold |
| `circuit_breaker`| object | Opens after repeated failures, rejecting requests for a cooldown period |

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
| `async`        | bool   | Fire-and-forget: child runs independently, parent does not wait. Child span kind is CONSUMER instead of CLIENT. Errors do not cascade to parent. Cannot combine with `retries` or `timeout` |

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

### events

Span events are timestamped annotations emitted during an operation's span via
`span.AddEvent()`. Use them for cache misses, query starts, connection
acquisitions, message receipts, and similar intra-span occurrences.

| Field        | Type   | Description |
|-------------|--------|-------------|
| `name`       | string | Event name (required) |
| `delay`      | string | Offset from span start time (Go duration, default: `0`) |
| `attributes` | map    | Event attributes — same generators as operation attributes |

```yaml
events:
  - name: cache.miss
    delay: 5ms
    attributes:
      cache.key:
        value: "user:*"
  - name: db.query.start
    delay: 10ms
```

### links

Span links represent non-parent-child relationships between spans — a consumer
linking back to the producer that enqueued a message, a batch job linking to
the requests it aggregates. Each entry is a `service.operation` reference.

The engine maintains a registry of the most recent span context for each
operation. When a span with links is created, it looks up each linked
operation's most recent span context and attaches it via `trace.WithLinks()`.
The first trace has no links (the registry is empty); subsequent traces link to
the most recent span — this mirrors real-world behaviour where the consumer
trails the producer.

```yaml
services:
  producer:
    operations:
      enqueue:
        duration: 5ms
  consumer:
    operations:
      dequeue:
        duration: 15ms
        links:
          - producer.enqueue
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
  values: { 200: 95, 404: 3, 500: 2 }
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
| `override` | map    | Per-operation overrides keyed by `service.operation`, or per-service overrides keyed by service name |
| `traffic`  | object | Traffic pattern override for this window |

Each operation override can set `duration`, `error_rate`, `attributes`, and
`metrics`. Overlapping scenarios merge overrides by priority — higher-priority
values win per field, attributes and metrics merge per key.

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

A `metrics` override replaces the `value` distribution of a topology-defined
metric for the scenario window. The metric must be defined with a `value` at
the same scope — a service name key overrides service-level metrics, a
`service.operation` key overrides operation-level metrics. Span-derived
metrics (no `value`) cannot be overridden, and `name`, `type`, and `unit` are
fixed at instrument creation time. An override keyed by a bare service name
may only contain `metrics`.

```yaml
scenarios:
  - name: cpu spike
    at: +2m
    duration: 5m
    override:
      gateway:
        metrics:
          gateway.cpu.utilisation:
            value: 0.95 +/- 0.02
```

### metrics

Topology-driven metric instruments. Define them at the service level (fire for
every span in the service) or at the operation level (fire only for that
operation). Requires `--signals metrics` or `--signals traces,metrics` when
running; if no metrics are defined the signal is silently empty.

| Field        | Type   | Description |
|-------------|--------|-------------|
| `name`       | string | OTel instrument name (required) |
| `type`       | string | `counter`, `updowncounter`, `histogram`, or `gauge` (required) |
| `unit`       | string | OTel unit string, e.g. `ms`, `s`, `{request}` (optional) |
| `value`      | string | Distribution to sample, e.g. `0.65` or `0.65 +/- 0.1`. Omit for span-derived behaviour (see below) |
| `interval`   | string | Emit on a timer instead of per span, e.g. `10s`. Requires `value`; not valid for gauges (see below) |
| `walk`       | string | Gauge only: mean-reversion timescale for a random walk, e.g. `30s` (see below) |
| `min`, `max` | float  | Gauge only: clamp observed values to these bounds |
| `attributes` | map    | Per-measurement attribute generators — same syntax as span attributes |

**Span-derived behaviour (no `value`):** the instrument records a value derived
from the span being observed.

| Type | Span-derived recording |
|------|----------------------|
| `counter` | `+1` per completed span |
| `updowncounter` | `+1` on span start, `−1` on span end — tracks active-span count |
| `histogram` | span duration, converted to the configured `unit` |
| `gauge` | not valid — gauge requires `value` |

**Topology-defined behaviour (`value` present):** the value is sampled from a
normal distribution on each observation.

| Type | Topology-defined recording |
|------|---------------------------|
| `counter` | sampled float added per completed span (clamped ≥ 0) |
| `updowncounter` | sampled float added per completed span |
| `histogram` | sampled float recorded per completed span |
| `gauge` | sampled float reported on each collection cycle |

**Random-walk gauges (`walk`):** by default each gauge collection samples
independently, producing white noise. Setting `walk` turns the gauge into a
mean-reverting random walk (an Ornstein-Uhlenbeck process): consecutive
samples are correlated and drift smoothly, while the long-run mean and
standard deviation still match `value`. The timescale controls how quickly
the value reverts to the mean — short timescales (a few seconds) look jittery,
long ones (minutes) drift slowly. Combine with `min`/`max` to keep bounded
metrics like utilisation in range:

```yaml
- name: gateway.cpu.utilisation
  type: gauge
  value: 0.65 +/- 0.1
  walk: 30s
  min: 0
  max: 1
```

Walk timescales relate to wall-clock time between collection cycles,
regardless of simulated time compression.

**Interval-driven metrics (`interval`):** by default, topology-defined
counters, updowncounters, and histograms record when a span fires, coupling
their emission rate to the trace rate. Setting `interval` decouples them: the
instrument records a sampled value on its own timer, so a service with no
traffic still emits metric data. Intervals are wall-clock durations,
regardless of simulated time compression. Gauges already behave this way (the
observable callback fires on the collection cycle) and do not accept
`interval`; span-derived metrics (no `value`) remain span-coupled.

```yaml
- name: gateway.gc.pause.duration
  type: histogram
  unit: ms
  value: 2.5 +/- 0.8
  interval: 10s   # emit every 10 seconds, regardless of trace rate
```

```yaml
services:
  gateway:
    metrics:
      # Span-derived histogram: records span duration in milliseconds
      - name: http.server.request.duration
        type: histogram
        unit: ms
      # Span-derived updowncounter: tracks currently active requests
      - name: http.server.active_requests
        type: updowncounter
      # Topology-defined gauge: independent of spans, sampled on collection
      - name: gateway.cpu.utilisation
        type: gauge
        value: 0.65 +/- 0.1
    operations:
      handle:
        duration: 50ms +/- 10ms
        metrics:
          # Operation-level metric: only fires for this operation
          - name: gateway.cache.hit_ratio
            type: gauge
            value: 0.85 +/- 0.05
```

**Migrating from the built-in instruments:** earlier versions of motel emitted
three hard-coded instruments automatically (`motel.span.duration`,
`motel.span.count`, `motel.span.errors`). These have been replaced by
topology-driven metrics. To restore equivalent behaviour, add the following to
each service that previously produced those instruments:

```yaml
metrics:
  - name: motel.span.duration
    type: histogram
    unit: ms
  - name: motel.span.count
    type: counter
  - name: motel.span.errors
    type: counter
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
