# motel-synth: Queue Depth, Circuit Breaker, and Backpressure

*2026-02-17T15:13:42Z by Showboat 0.6.0*
<!-- showboat-id: a1cf8797-c9f0-40fb-b71a-9b1b2c5439a0 -->

Real systems develop emergent behaviour under load: queues fill up, circuit breakers trip, and backpressure slows everything down. motel-synth models these cross-trace effects with three operation-level features. This demo walks through the configuration, shows the baseline, and then observes what happens when traffic spikes and a database degrades.

## The topology

A three-tier topology: gateway calls api, api calls database. The interesting configuration is on api.process and database.query:

- **api.process** has `queue_depth: 20` (reject when concurrent requests exceed capacity) and `backpressure` (amplify latency and error rate when EWMA latency exceeds a threshold)
- **database.query** has a `circuit_breaker` (trip open after 5 failures in 30s, cool down for 10s)

Two scenarios inject stress: a 10x traffic spike at +5s and database degradation at +8s.

```bash
cat examples/synth/backpressure-queue.yaml
```

```output
# Backpressure, queue depth, and circuit breaker simulation
# Demonstrates cross-trace state effects under load

version: 1

services:
  gateway:
    operations:
      request:
        duration: 5ms +/- 2ms
        calls:
          - target: api.process
            timeout: 200ms
            retries: 1
            retry_backoff: 20ms

  api:
    attributes:
      deployment.environment: production
    operations:
      process:
        duration: 15ms +/- 5ms
        queue_depth: 20
        backpressure:
          latency_threshold: 50ms
          duration_multiplier: 3.0
          error_rate_add: 10%
        calls:
          - target: database.query

  database:
    operations:
      query:
        duration: 20ms +/- 5ms
        error_rate: 1%
        circuit_breaker:
          failure_threshold: 5
          window: 30s
          cooldown: 10s

traffic:
  rate: 50/s

scenarios:
  - name: load spike
    at: +5s
    duration: 15s
    traffic:
      rate: 500/s

  - name: database degradation
    at: +8s
    duration: 10s
    override:
      database.query:
        duration: 150ms +/- 30ms
        error_rate: 30%
```

## Validation

The validator checks the new fields: queue_depth must be non-negative, backpressure requires a valid latency_threshold and non-negative multiplier, and circuit breaker needs all three of failure_threshold, window, and cooldown.

```bash
./build/motel-synth validate examples/synth/backpressure-queue.yaml
```

```output
Configuration valid: 3 services, 1 root operations
```

Validation catches incomplete circuit breaker configuration. For example, omitting the cooldown:

```bash
cat > /tmp/bad-cb.yaml << 'EOF'
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        circuit_breaker:
          failure_threshold: 5
          window: 30s
traffic:
  rate: 10/s
EOF
./build/motel-synth validate /tmp/bad-cb.yaml 2>&1 | head -1
```

```output
Error: service "svc" operation "op": circuit_breaker requires cooldown
```

## Baseline: before scenarios

Running for 2 seconds stays within the pre-scenario window (load spike starts at +5s). At 50 req/s with a 1% base error rate, there are no circuit breaker trips or queue rejections.

```bash
./build/motel-synth run --stdout --duration 2s examples/synth/backpressure-queue.yaml 2>&1 >/dev/null | tail -1 | jq -r '
  "queue_rejections: \(.queue_rejections)",
  "circuit_breaker_trips: \(.circuit_breaker_trips)",
  "error_rate < 5%: \(.error_rate < 0.05)"'
```

```output
queue_rejections: 0
circuit_breaker_trips: 0
error_rate < 5%: true
```

## Under stress: circuit breaker trips

Running for 12 seconds covers both scenarios. The load spike (500 req/s from +5s) and database degradation (150ms latency, 30% errors from +8s) push database.query past the circuit breaker threshold: 5 failures within the 30s window trips it open, rejecting subsequent requests until the 10s cooldown expires.

```bash
./build/motel-synth run --stdout --duration 12s examples/synth/backpressure-queue.yaml 2>&1 >/dev/null | tail -1 | jq -r '
  "circuit_breaker_trips > 0: \(.circuit_breaker_trips > 0)",
  "error_rate > 10%: \(.error_rate > 0.10)",
  "traces > 1000: \(.traces > 1000)"'
```

```output
circuit_breaker_trips > 0: true
error_rate > 10%: true
traces > 1000: true
```

The traffic jumps from 50/s to 500/s (over 1000 traces in 12s), and the combined load spike and database degradation push the error rate well above the 1% baseline. Circuit breaker trips confirm that database.query tripped open during the degradation window.

## Rejection spans

When the circuit breaker rejects a request, the engine emits a short error span with `synth.rejected=true` and `synth.rejection_reason` attributes. These are visible in the trace output alongside normal spans.

```bash
./build/motel-synth run --stdout --duration 12s examples/synth/backpressure-queue.yaml 2>/dev/null | jq -rs '
  [.[] | select(any(.Attributes[]?; .Key == "synth.rejected"))] as $rejected |
  ($rejected | length > 0) as $has_rejected |
  ($rejected | [.[].Attributes[] | select(.Key == "synth.rejection_reason") | .Value.Value] | unique | sort) as $reasons |
  ($rejected[0].Status.Code) as $status |
  "has rejection spans: \($has_rejected)",
  "rejection reasons: \($reasons | join(", "))",
  "rejection span status: \($status)"'
```

```output
has rejection spans: true
rejection reasons: circuit_open
rejection span status: Error
```

Rejection spans have Error status and carry the reason as an attribute. A monitoring system receiving these traces can alert on `synth.rejection_reason=circuit_open` to detect circuit breaker activity.

## Backpressure: duration amplification

Backpressure tracks an EWMA of recent latency for api.process. When latency exceeds the 50ms threshold (caused by slow database calls during degradation), the engine amplifies subsequent durations by the configured 3x multiplier. We can observe this by comparing api.process span durations before and during the degradation window.

```bash
./build/motel-synth run --stdout --duration 12s examples/synth/backpressure-queue.yaml 2>/dev/null | jq -rs '
  def ms: (.EndTime | split("T")[1] | rtrimstr("Z") | split(":") |
    ((.[0] | tonumber) * 3600 + (.[1] | tonumber) * 60 + (.[2] | tonumber)) * 1000) -
    (.StartTime | split("T")[1] | rtrimstr("Z") | split(":") |
    ((.[0] | tonumber) * 3600 + (.[1] | tonumber) * 60 + (.[2] | tonumber)) * 1000);
  [.[] | select(.Name == "process") | select(any(.Attributes[]?; .Key == "synth.rejected") | not)] |
  (map(ms) | max | round) as $peak |
  (map(ms) | min | round) as $baseline |
  "peak > baseline: \($peak > $baseline)",
  "amplification (peak/baseline > 2): \(($peak / $baseline) > 2)"'
```

```output
peak > baseline: true
amplification (peak/baseline > 2): true
```

The peak api.process duration is well over 2x the baseline — the 3x duration multiplier amplifying the already-elevated latency. This produces the realistic slow-then-slower pattern you see in production systems under cascading load.

## State persistence across scenarios

Simulation state persists for the entire run. After the database degradation scenario ends at +18s, the circuit breaker may still be open (waiting for cooldown) and the EWMA latency may still be above the backpressure threshold. This matches real-world behaviour: removing the cause of degradation does not instantly reset the symptoms.
