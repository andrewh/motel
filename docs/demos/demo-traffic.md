# motel: Traffic Patterns

*2026-02-11T16:00:00Z*

motel supports four traffic arrival patterns that control how traces are generated over time. This demo compares them using the same base rate and topology.

## The topology

A minimal two-service topology with a 50/s base rate. The `pattern` field selects the arrival model.

```bash
cat docs/examples/traffic-patterns.yaml
```

```output
# Minimal topology for demonstrating traffic patterns
# Two services, one call — keeps output easy to read

version: 1
services:
  api:
    operations:
      request:
        duration: 10ms +/- 3ms
        error_rate: 1%
        calls:
          - database.query

  database:
    operations:
      query:
        duration: 5ms +/- 2ms
        error_rate: 0.1%

traffic:
  rate: 50/s
  pattern: uniform
```

## Uniform pattern

The default. Generates traces at a constant rate throughout the run.

```bash
motel run --stdout --duration 1s docs/examples/traffic-patterns.yaml 2>&1 >/dev/null | tail -1 | jq -r \
  '"rate within 20% of 50/s: \(.traces_per_second >= 40 and .traces_per_second <= 60)"'
```

```output
rate within 20% of 50/s: true
```

## Diurnal pattern

Models a 24-hour day/night cycle using a sine wave. Peak rate is 1.5x the base at the 12-hour mark; the trough is 0.5x at hour 0. Since the simulation starts at hour 0, a short run sees only the trough rate.

```bash
cat > /tmp/diurnal.yaml << 'EOF'
version: 1
services:
  api:
    operations:
      request:
        duration: 10ms +/- 3ms
        calls:
          - db.query
  db:
    operations:
      query:
        duration: 5ms +/- 2ms
traffic:
  rate: 50/s
  pattern: diurnal
EOF
motel run --stdout --duration 1s /tmp/diurnal.yaml 2>&1 >/dev/null | tail -1 | jq -r \
  '"rate near 25/s (0.5x trough): \(.traces_per_second >= 15 and .traces_per_second <= 35)"'
```

```output
rate near 25/s (0.5x trough): true
```

## Bursty pattern

Alternates between the base rate and 5x bursts. Bursts last 30 seconds every 5 minutes. Since the burst window starts at second 0, a short run experiences the full burst multiplier.

```bash
cat > /tmp/bursty.yaml << 'EOF'
version: 1
services:
  api:
    operations:
      request:
        duration: 10ms +/- 3ms
        calls:
          - db.query
  db:
    operations:
      query:
        duration: 5ms +/- 2ms
traffic:
  rate: 50/s
  pattern: bursty
EOF
motel run --stdout --duration 1s /tmp/bursty.yaml 2>&1 >/dev/null | tail -1 | jq -r \
  '"rate above 150/s (5x burst): \(.traces_per_second > 150)"'
```

```output
rate above 150/s (5x burst): true
```

## Comparing all three

Running the same topology with each pattern shows how arrival models affect throughput. The `--duration` flag overrides any config-level duration setting.

```bash
for p in uniform diurnal bursty; do
cat > /tmp/cmp-${p}.yaml << EOF
version: 1
services:
  api:
    operations:
      request:
        duration: 10ms +/- 3ms
        calls:
          - db.query
  db:
    operations:
      query:
        duration: 5ms +/- 2ms
traffic:
  rate: 50/s
  pattern: ${p}
EOF
done
U=$(motel run --stdout --duration 1s /tmp/cmp-uniform.yaml 2>&1 >/dev/null | tail -1 | jq '.traces_per_second')
D=$(motel run --stdout --duration 1s /tmp/cmp-diurnal.yaml 2>&1 >/dev/null | tail -1 | jq '.traces_per_second')
B=$(motel run --stdout --duration 1s /tmp/cmp-bursty.yaml 2>&1 >/dev/null | tail -1 | jq '.traces_per_second')
jq -rn --argjson u "$U" --argjson d "$D" --argjson b "$B" '
  "bursty > uniform: \($b > $u)",
  "diurnal < uniform: \($d < $u)"'
```

```output
bursty > uniform: true
diurnal < uniform: true
```

The three patterns cover common load testing scenarios: steady state (uniform), natural traffic cycles (diurnal), and sudden traffic spikes (bursty).
