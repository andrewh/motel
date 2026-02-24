# motel: Structural Topology Checks

*2026-02-24T09:15:00Z by Showboat 0.6.1*
<!-- showboat-id: a9249d74-358d-4905-83e7-831f3e455e94 -->

motel check runs structural analysis on a topology before you send any traffic. It computes worst-case trace depth, fan-out per span, and total spans per trace using static graph analysis, then validates these against configurable limits. Optional sampled exploration runs the engine with an in-memory exporter to measure empirical values and report percentile distributions (p50, p95, p99). This catches surprising span explosions from the interaction of count, retries, and conditional calls.

## Basic check

A five-service topology with two call chains: gateway to user-service to postgres, and gateway to order-service to postgres and redis. All checks pass comfortably within the defaults.

```bash
build/motel check docs/examples/basic-topology.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.GET /users → user-service.list → postgres.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 2 (limit: 100)
      worst: order-service.create
      p50: 1  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-spans: 4 static worst-case, 4 observed/1000 samples (limit: 10000)
      p50: 3  p95: 4  p99: 4  max: 4  (1000 samples)
```

The depth of 2 means the longest call chain has 2 hops from root to leaf. The path shows which operations form it. Fan-out of 2 is at order-service.create, which calls both postgres.query and redis.get in parallel. The fan-out p50 of 1 shows that half the sampled traces go through gateway.GET /users, which has only one downstream call. The static worst-case span count of 4 matches the observed max — this topology has no retries or probabilistic calls, so every trace is identical. The p50 of 3 reflects traces through the shorter gateway → user-service → postgres chain.

## Retries amplify fan-out and span count

The cascading failure topology has retries on both call edges: gateway retries api.process once, and api.process retries database.query twice. Static analysis accounts for the worst case where every attempt fires.

```bash
build/motel check --seed 42 docs/examples/cascading-failure.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 3 (limit: 100)
      worst: api.process
      p50: 1  p95: 1  p99: 2  max: 2  (1000 samples)
PASS  max-spans: 9 static worst-case, 4 observed/1000 samples (limit: 10000)
      p50: 3  p95: 3  p99: 4  max: 4  (1000 samples)
```

Fan-out at api.process is 3: one call to database.query with 2 retries means up to 3 attempts. The static worst-case of 9 spans comes from gateway (1) calling api.process with 1 retry (2 attempts × (1 api span + 3 query attempts)) = 1 + 2×4 = 9. But sampling shows only 4 observed — most traces hit no errors and never retry. The percentiles tell the same story: p50 fan-out is 1 (no retries), p99 is 2 (occasional retry). The gap between static (9) and observed (4) is the safety margin that check quantifies.

## Failing checks

Custom limits catch topologies that exceed your thresholds. The exit code is 1 when any check fails, so this works as a CI gate.

```bash
build/motel check --max-depth 1 docs/examples/basic-topology.yaml 2>&1; echo "exit code: $?"
```

```output
FAIL  max-depth: 2 (limit: 1)
      path: gateway.GET /users → user-service.list → postgres.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 2 (limit: 100)
      worst: order-service.create
      p50: 1  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-spans: 4 static worst-case, 4 observed/1000 samples (limit: 10000)
      p50: 3  p95: 4  p99: 4  max: 4  (1000 samples)
Error: one or more checks failed
exit code: 1
```

The depth limit of 1 fails because the topology has a 2-hop chain. The path output shows exactly which operations need restructuring.

## Span explosion from count × retries

When count and retries interact across multiple levels, span counts multiply fast. This topology has gateway calling api.process 5 times with 3 retries, and api.process calling database.query 4 times with 2 retries.

```bash
cat > /tmp/span-explosion.yaml << 'EOF'
version: 1
services:
  gateway:
    operations:
      request:
        duration: 5ms +/- 2ms
        calls:
          - target: api.process
            count: 5
            retries: 3
  api:
    operations:
      process:
        duration: 10ms +/- 3ms
        calls:
          - target: database.query
            count: 4
            retries: 2
  database:
    operations:
      query:
        duration: 3ms +/- 1ms
        error_rate: 10%
traffic:
  rate: 10/s
EOF
echo 'wrote /tmp/span-explosion.yaml'
```

```output
wrote /tmp/span-explosion.yaml
```

```bash
build/motel check --seed 42 /tmp/span-explosion.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 20 (limit: 100)
      worst: gateway.request
      p50: 5  p95: 7  p99: 7  max: 8  (1000 samples)
PASS  max-spans: 261 static worst-case, 39 observed/1000 samples (limit: 10000)
      p50: 28  p95: 31  p99: 35  max: 39  (1000 samples)
```

The static worst-case is 261 spans: gateway (1) + 5 calls × (1 + 3 retries) api attempts (20), each producing 1 api span + 4 calls × (1 + 2 retries) query attempts (12) = 1 + 20 × 13 = 261. But observed max is only 39, and the p50 is 28 — retries only fire on errors, and with a 10% error rate most calls succeed on the first attempt. The percentile spread (p50: 28, p95: 31, p99: 35, max: 39) shows that even the tail is far below the static ceiling. The roughly 7x gap between worst-case and typical is what makes this check valuable: it tells you the ceiling your collector must handle.

Setting a tight max-spans limit catches this:

```bash
build/motel check --max-spans 50 --seed 42 /tmp/span-explosion.yaml 2>&1; echo "exit code: $?"
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 20 (limit: 100)
      worst: gateway.request
      p50: 5  p95: 7  p99: 7  max: 8  (1000 samples)
FAIL  max-spans: 261 static worst-case, 39 observed/1000 samples (limit: 50)
      p50: 28  p95: 31  p99: 35  max: 39  (1000 samples)
Error: one or more checks failed
exit code: 1
```

## Fan-out limits

The max-fan-out check catches wide spans — operations that produce many direct children. This matters for collector memory and for UI readability in trace viewers.

```bash
build/motel check --max-fan-out 10 --seed 42 /tmp/span-explosion.yaml 2>&1; echo "exit code: $?"
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
FAIL  max-fan-out: 20 (limit: 10)
      worst: gateway.request
      p50: 5  p95: 7  p99: 7  max: 8  (1000 samples)
PASS  max-spans: 261 static worst-case, 39 observed/1000 samples (limit: 10000)
      p50: 28  p95: 31  p99: 35  max: 39  (1000 samples)
Error: one or more checks failed
exit code: 1
```

Gateway.request has a static fan-out of 20: 5 calls × (1 + 3 retries) = 20 child spans. But the percentiles show typical fan-out is much lower: p50 is 5 (no retries), climbing to 7 at p95 as occasional retries fire. The "worst" field identifies exactly which operation to fix.

## Static-only analysis

Set `--samples 0` to skip sampled exploration entirely. This is faster and sufficient when you only care about worst-case bounds. No percentile lines are shown.

```bash
build/motel check --samples 0 docs/examples/basic-topology.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.GET /users → user-service.list → postgres.query
PASS  max-fan-out: 2 (limit: 100)
      worst: order-service.create
PASS  max-spans: 4 static worst-case (limit: 10000)
```

The "observed" column and percentile lines disappear when sampling is off.

## Extended sampling

Increase --samples for higher confidence in the observed values. With probabilistic calls and low error rates, rare paths may not appear in 1,000 samples. Extended sampling finds them.

```bash
build/motel check --samples 10000 --seed 42 /tmp/span-explosion.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (10000 samples)
PASS  max-fan-out: 20 (limit: 100)
      worst: gateway.request
      p50: 5  p95: 7  p99: 7  max: 10  (10000 samples)
PASS  max-spans: 261 static worst-case, 43 observed/10000 samples (limit: 10000)
      p50: 28  p95: 31  p99: 35  max: 43  (10000 samples)
```

With 10,000 samples the observed max climbs from 39 to 43, and fan-out max reaches 10 (up from 8) — rare retry storms produce wider spans. The p50 and p95 are stable across both sample sizes, confirming they represent the typical behaviour. The static bound of 261 still holds, confirming that the analysis is conservative.

## Reproducible results with --seed

A fixed --seed makes sampled results deterministic. Running the same command twice produces identical output, which is useful for CI and for reproducing specific findings.

```bash
build/motel check --seed 12345 docs/examples/cascading-failure.yaml
build/motel check --seed 12345 docs/examples/cascading-failure.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 3 (limit: 100)
      worst: api.process
      p50: 1  p95: 1  p99: 1  max: 2  (1000 samples)
PASS  max-spans: 9 static worst-case, 4 observed/1000 samples (limit: 10000)
      p50: 3  p95: 3  p99: 3  max: 4  (1000 samples)
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 3 (limit: 100)
      worst: api.process
      p50: 1  p95: 1  p99: 1  max: 2  (1000 samples)
PASS  max-spans: 9 static worst-case, 4 observed/1000 samples (limit: 10000)
      p50: 3  p95: 3  p99: 3  max: 4  (1000 samples)
```

## Conditional calls

The conditional-calls topology has on-success, on-error, and probabilistic calls plus a count:3 fan-out. Static analysis conservatively includes all paths.

```bash
build/motel check --seed 42 docs/examples/conditional-calls.yaml
```

```output
PASS  max-depth: 1 (limit: 10)
      path: gateway.GET /users → redis.get
      p50: 1  p95: 1  p99: 1  max: 1  (1000 samples)
PASS  max-fan-out: 3 (limit: 100)
      worst: gateway.GET /users
      p50: 3  p95: 3  p99: 3  max: 3  (1000 samples)
PASS  max-spans: 4 static worst-case, 4 observed/1000 samples (limit: 10000)
      p50: 4  p95: 4  p99: 4  max: 4  (1000 samples)
```

Static analysis says 4 worst-case spans for GET /users: the gateway span plus redis.get (on-success), postgres.query (on-error), and audit.log (probability 0.1). A real trace can never have both redis.get and postgres.query since they are mutually exclusive conditions, but static analysis counts both paths conservatively. POST /batch has its own 3-span fan-out (count:3 to worker.process), but total spans for that root are still 4.

## Multiple failing checks

All three checks can fail simultaneously. The output shows every check result regardless of pass/fail, so you see the full picture at once.

```bash
build/motel check --max-depth 1 --max-fan-out 10 --max-spans 50 --seed 42 /tmp/span-explosion.yaml 2>&1; echo "exit code: $?"
```

```output
FAIL  max-depth: 2 (limit: 1)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
FAIL  max-fan-out: 20 (limit: 10)
      worst: gateway.request
      p50: 5  p95: 7  p99: 7  max: 8  (1000 samples)
FAIL  max-spans: 261 static worst-case, 39 observed/1000 samples (limit: 50)
      p50: 28  p95: 31  p99: 35  max: 39  (1000 samples)
Error: one or more checks failed
exit code: 1
```

## Capping spans per sampled trace

By default, sampled traces are bounded at 10,000 spans (motel's safety limit). For topologies with large worst-case span counts, this means observed values plateau at 10,000 regardless of how wide the trace could grow. The `--max-spans-per-trace` flag lets you lower this cap to see how the engine behaves under a tighter constraint.

Without the flag, the span-explosion topology from earlier observes up to 39 spans:

```bash
build/motel check --seed 99 /tmp/span-explosion.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 20 (limit: 100)
      worst: gateway.request
      p50: 5  p95: 7  p99: 7  max: 8  (1000 samples)
PASS  max-spans: 261 static worst-case, 39 observed/1000 samples (limit: 10000)
      p50: 28  p95: 31  p99: 35  max: 39  (1000 samples)
```

With a cap of 20, observed spans plateau at the limit:

```bash
build/motel check --max-spans-per-trace 20 --seed 99 /tmp/span-explosion.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.request → api.process → database.query
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 20 (limit: 100)
      worst: gateway.request
      p50: 5  p95: 6  p99: 7  max: 8  (1000 samples)
PASS  max-spans: 261 static worst-case, 20 observed/1000 samples (limit: 10000)
      p50: 20  p95: 20  p99: 20  max: 20  (1000 samples)
```

The static worst-case is unchanged — it comes from graph analysis, not sampling. But the observed value drops from 39 to 20 because the engine stops generating spans once a trace hits the cap. The span percentiles flatten to 20 across the board, confirming that every trace hits the cap. Fan-out percentiles shift slightly because the cap truncates traces before all children are emitted.

This is the same `--max-spans-per-trace` flag that `motel run` uses. During `run`, traces that hit the cap are counted in the `spans_bounded` stat:

```bash
build/motel run --max-spans-per-trace 20 --duration 2s --stdout /tmp/span-explosion.yaml > /dev/null
```

```output
{"traces":20,"spans":400,"errors":24,"failed_traces":0,"timeouts":0,"retries":24,"spans_bounded":20,"queue_rejections":0,"circuit_breaker_trips":0,...}
```

All 20 traces hit the 20-span cap (`spans_bounded: 20`), producing exactly 400 total spans (`traces` and `spans_bounded` are stable; `errors` and `retries` vary between runs). Without the cap, the same topology generates more spans per trace and `spans_bounded` stays at 0:

```bash
build/motel run --duration 2s --stdout /tmp/span-explosion.yaml > /dev/null
```

```output
{"traces":20,"spans":587,"errors":61,"failed_traces":0,"timeouts":0,"retries":59,"spans_bounded":0,"queue_rejections":0,"circuit_breaker_trips":0,...}
```

Use `check --max-spans-per-trace` to preview how the cap affects observed span counts before committing to a limit in `run`.

## Error handling

`check` validates the topology before analysis, so invalid topology files are caught early.

```bash
build/motel check /tmp/nonexistent.yaml 2>&1; echo "exit code: $?"
```

```output
Error: reading config: open /tmp/nonexistent.yaml: no such file or directory
exit code: 1
```

```bash
cat > /tmp/bad-topology.yaml << 'EOF'
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        calls:
          - nonexistent.op
traffic:
  rate: 10/s
EOF
build/motel check /tmp/bad-topology.yaml 2>&1; echo "exit code: $?"
```

```output
Error: service "svc" operation "op": call "nonexistent.op" references unknown operation
exit code: 1
```

```bash
build/motel check 2>&1; echo "exit code: $?"
```

```output
Error: missing topology file or URL

Usage: motel check <topology.yaml | URL>
exit code: 1
```
