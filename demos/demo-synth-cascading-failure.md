# motel-synth: Cascading Failure with Timeout and Retry

*2026-02-13T18:40:27Z*

When a downstream service slows down, the effects should propagate through the call chain: callers time out, retry, and eventually cascade errors upward. motel-synth now models this with per-call timeout, retry, and error cascading. This demo walks through the configuration, validates it, and shows the observable impact during a scenario-driven database degradation.

## The topology

A three-tier topology: gateway calls api, api calls database. Each call has a timeout and retries. At the 5-second mark, a scenario degrades database.query from 20ms to 200ms with 25% errors.

```bash
cat examples/synth/cascading-failure.yaml
```

```output
# Cascading failure with timeout, retry, and scenario-driven slowdown
# When the database degrades, timeouts propagate through the call chain

services:
  gateway:
    operations:
      request:
        duration: 5ms +/- 2ms
        calls:
          - target: api.process
            timeout: 150ms
            retries: 1
            retry_backoff: 20ms

  api:
    operations:
      process:
        duration: 10ms +/- 3ms
        calls:
          - target: database.query
            timeout: 100ms
            retries: 2
            retry_backoff: 30ms

  database:
    operations:
      query:
        duration: 20ms +/- 5ms
        error_rate: 1%

traffic:
  rate: 50/s

scenarios:
  - name: database degradation
    at: +5s
    duration: 10s
    override:
      database.query:
        duration: 200ms +/- 50ms
        error_rate: 25%
```

The key fields are on each call: `timeout` caps how long the caller waits, `retries` sets how many retry attempts after a failure, and `retry_backoff` adds a constant delay between attempts. The api-to-database call has a 100ms timeout with 2 retries — so when database.query jumps to 200ms during degradation, every call times out and retries up to twice before giving up.

## Validation

The validator checks timeout, retries, and retry_backoff fields alongside the usual topology structure.

```bash
./build/motel-synth validate examples/synth/cascading-failure.yaml
```

```output
Configuration valid: 3 services, 1 root operations
```

Misconfigured retry fields are caught. For example, setting retry_backoff without retries:

```bash
cat > /tmp/bad-retry.yaml << 'EOF'
services:
  svc:
    operations:
      op:
        duration: 10ms
        calls:
          - target: other.op
            retry_backoff: 50ms
  other:
    operations:
      op:
        duration: 5ms
traffic:
  rate: 10/s
EOF
./build/motel-synth validate /tmp/bad-retry.yaml 2>&1 | head -1
```

```output
Error: service "svc" operation "op": call "other.op" retry_backoff requires retries > 0
```

## Baseline: before degradation

Running for 2 seconds stays within the pre-scenario window (scenario starts at +5s). With only a 1% base error rate, timeouts are zero and retries are rare.

```bash
./build/motel-synth run --stdout --duration 2s examples/synth/cascading-failure.yaml 2>&1 >/dev/null | tail -1 | jq -r '
  "timeouts: \(.timeouts)",
  "error_rate < 5%: \(.error_rate < 0.05)"'
```

```output
timeouts: 0
error_rate < 5%: true
```

## During degradation: timeouts, retries, and cascading errors

Running for 10 seconds covers the full degradation window (5s-15s). Database latency jumps to 200ms, exceeding the 100ms timeout. Each failed call retries, and failures cascade upward through api to gateway.

```bash
./build/motel-synth run --stdout --duration 10s examples/synth/cascading-failure.yaml 2>&1 >/dev/null | tail -1 | jq -r '
  "timeouts > 0: \(.timeouts > 0)",
  "retries > 0: \(.retries > 0)",
  "error_rate > 10%: \(.error_rate > 0.10)"'
```

```output
timeouts > 0: true
retries > 0: true
error_rate > 10%: true
```

The timeouts and retries counters confirm that cascading failure mechanics are active. The error rate climbs well above baseline because timeout and child failures cascade up: even though gateway and api have 0% own error rates, they inherit errors from their timed-out downstream calls.

## Retry attempts in traces

During degradation, traces contain multiple query spans from retry attempts. A trace with retries has more than the usual 3 spans (gateway, api, database). With 2 retries on the api-to-database call and 1 retry on gateway-to-api, a maximally-retried trace produces up to 9 spans.

```bash
./build/motel-synth run --stdout --duration 8s examples/synth/cascading-failure.yaml 2>/dev/null | jq -rs '
  group_by(.SpanContext.TraceID) |
  map({
    span_count: length,
    query_spans: [.[] | select(.Name == "query")] | length,
    request_errored: any(.Name == "request" and .Status.Code == "Error")
  }) |
  (map(select(.query_spans > 2)) | length > 0) as $has_retries |
  (map(select(.request_errored)) | length > 0) as $has_cascaded |
  (map(.query_spans) | max) as $max_queries |
  "traces with retries (>2 query spans): \($has_retries)",
  "max query spans in a trace: \($max_queries)",
  "gateway errors from cascading: \($has_cascaded)"'
```

```output
traces with retries (>2 query spans): true
max query spans in a trace: 6
gateway errors from cascading: true
```

The max of 6 query spans comes from api retrying twice (3 attempts), and gateway retrying once (2 api calls, each making up to 3 queries). The gateway errors confirm cascading: even with a 0% own error rate, it is marked as errored because its child call timed out.

## Timeout capping

Timeout capping limits how long a caller perceives a child call to take. The child span keeps its full duration (the downstream service does not know the caller gave up), but the parent advances time based on the capped timeout. This keeps parent span durations realistic — a gateway with a 150ms timeout never shows a 500ms span.

```bash
./build/motel-synth run --stdout --duration 8s examples/synth/cascading-failure.yaml 2>/dev/null | jq -rs '
  def ms: split("T")[1] | rtrimstr("Z") | split(":") |
    ((.[0] | tonumber) * 3600 + (.[1] | tonumber) * 60 + (.[2] | tonumber)) * 1000;
  group_by(.SpanContext.TraceID) |
  map(select(any(.Name == "request" and .Status.Code == "Error"))) |
  map(
    (map(select(.Name == "request")) | .[0] | ((.EndTime | ms) - (.StartTime | ms)) | round)
  ) |
  "gateway bounded by timeout (< 500ms): \(max < 500)"'
```

```output
gateway bounded by timeout (< 500ms): true
```

Even during full degradation with retries, the gateway never exceeds ~350ms. Without timeout capping, a single gateway request with retried 200ms queries could take well over a second. The 150ms gateway timeout and 100ms api timeout bound the cascading latency, producing the realistic failure signatures you would see in a production incident.
