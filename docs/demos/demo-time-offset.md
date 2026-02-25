# motel: Time Offset

_2026-02-25T08:34:44Z by Showboat 0.6.1_

<!-- showboat-id: 74c2e19b-e96e-4f18-84df-aa7b7ecfd5fb -->

The `--time-offset` flag shifts all trace span and log record timestamps by a fixed duration. Negative values move timestamps into the past; positive values move them into the future. This is useful for testing late-arrival handling in collectors, retention policy enforcement in backends, backfill pipelines, and out-of-order ingestion.

## The topology

A minimal single-service topology keeps the output easy to read.

```bash
cat docs/examples/minimal.yaml
```

```output
# Smallest valid motel topology
# Run with: motel run --stdout minimal.yaml

version: 1

services:
  api:
    operations:
      GET /health:
        duration: 30ms +/- 10ms

traffic:
  rate: 1/s
```

## Baseline: no offset

Without `--time-offset`, span timestamps are close to the current wall-clock time.

```bash
NOW=$(date -u +%Y-%m-%dT%H); build/motel run --stdout --duration 1s docs/examples/minimal.yaml 2>/dev/null | head -1 | jq -r --arg now "$NOW" '"starts with current hour: \(.StartTime | startswith($now))"'
```

```output
starts with current hour: true
```

## Shifting into the past

Pass a negative duration to `--time-offset` to generate traces that appear to have happened earlier. Here, `-24h` produces spans timestamped a day ago.

```bash
YESTERDAY=$(date -u -v-1d +%Y-%m-%dT%H); build/motel run --stdout --duration 1s --time-offset=-24h docs/examples/minimal.yaml 2>/dev/null | head -1 | jq -r --arg y "$YESTERDAY" '"starts with yesterday: \(.StartTime | startswith($y))"'
```

```output
starts with yesterday: true
```

## Shifting into the future

A positive offset produces spans with timestamps ahead of the current time.

```bash
TOMORROW=$(date -u -v+1d +%Y-%m-%dT%H); build/motel run --stdout --duration 1s --time-offset=24h docs/examples/minimal.yaml 2>/dev/null | head -1 | jq -r --arg t "$TOMORROW" '"starts with tomorrow: \(.StartTime | startswith($t))"'
```

```output
starts with tomorrow: true
```

## Log timestamps follow the offset

When emitting logs alongside traces, log record timestamps are shifted by the same offset. The `--slow-threshold` flag triggers WARN-level log records for slow spans, making it easy to verify.

```bash
YESTERDAY=$(date -u -v-1d +%Y-%m-%dT%H); build/motel run --stdout --duration 1s --signals logs --slow-threshold 1ms --time-offset=-24h docs/examples/minimal.yaml 2>/dev/null | head -1 | jq -r --arg y "$YESTERDAY" '"log timestamp starts with yesterday: \(.Timestamp | startswith($y))"'
```

```output
log timestamp starts with yesterday: true
```

## Metric timestamps are not shifted

The OpenTelemetry Metrics API does not support caller-supplied timestamps. Metric data points are timestamped at collection time by the SDK's PeriodicReader. This is a limitation of the [Metrics API spec](https://opentelemetry.io/docs/specs/otel/metrics/api/), and will be addressed for motel in [issue 99](https://github.com/andrewh/motel/issues/99).

## Scenario timing is unaffected

The offset only shifts exported signal timestamps. Scenario activation still uses real wall-clock elapsed time. This means `--time-offset` changes what the outside world sees without altering which scenario overrides are active at any point during the run.

## Use cases

- **Late-arrival testing**: Use a negative offset to generate traces that arrive at a collector well after their timestamps, exercising late-arrival windows and out-of-order handling.
- **Retention policy verification**: Produce traces with timestamps older than the configured retention period to confirm a backend correctly expires or reject them.
- **Backfill simulation**: Generate a batch of historical-looking traces to test ingestion pipelines that handle data from past time ranges.
- **Clock skew modelling**: Apply a small offset to simulate services with drifted clocks, useful for testing timestamp reconciliation in distributed tracing.
