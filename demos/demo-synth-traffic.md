# motel-synth: Traffic Patterns

*2026-02-11T16:00:00Z*

motel-synth supports four traffic arrival patterns that control how traces are generated over time. This demo compares them using the same base rate and topology.

## The topology

A minimal two-service topology with a 50/s base rate. The `pattern` field selects the arrival model.

```bash
cat examples/synth/traffic-patterns.yaml
```

```output
# Minimal topology for demonstrating traffic patterns
# Two services, one call — keeps output easy to read

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
./build/motel-synth run --stdout --duration 1s examples/synth/traffic-patterns.yaml 2>&1 >/dev/null | tail -1 | python3 -c "
import json, sys
stats = json.loads(sys.stdin.readline())
rate = stats['traces_per_second']
print('rate within 20% of 50/s:', 40 <= rate <= 60)
"
```

```output
rate within 20% of 50/s: True
```

## Diurnal pattern

Models a 24-hour day/night cycle using a sine wave. Peak rate is 1.5x the base at the 12-hour mark; the trough is 0.5x at hour 0. Since the simulation starts at hour 0, a short run sees only the trough rate.

```bash
cat > /tmp/diurnal.yaml << 'EOF'
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
./build/motel-synth run --stdout --duration 1s /tmp/diurnal.yaml 2>&1 >/dev/null | tail -1 | python3 -c "
import json, sys
stats = json.loads(sys.stdin.readline())
rate = stats['traces_per_second']
print('rate near 25/s (0.5x trough):', 15 <= rate <= 35)
"
```

```output
rate near 25/s (0.5x trough): True
```

## Poisson pattern

Constant mean rate with Poisson-distributed inter-arrival times. Over a full run, the throughput matches uniform, but individual intervals vary randomly.

```bash
cat > /tmp/poisson.yaml << 'EOF'
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
  pattern: poisson
EOF
./build/motel-synth run --stdout --duration 1s /tmp/poisson.yaml 2>&1 >/dev/null | tail -1 | python3 -c "
import json, sys
stats = json.loads(sys.stdin.readline())
rate = stats['traces_per_second']
print('rate within 20% of 50/s:', 40 <= rate <= 60)
"
```

```output
rate within 20% of 50/s: True
```

## Bursty pattern

Alternates between the base rate and 5x bursts. Bursts last 30 seconds every 5 minutes. Since the burst window starts at second 0, a short run experiences the full burst multiplier.

```bash
cat > /tmp/bursty.yaml << 'EOF'
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
./build/motel-synth run --stdout --duration 1s /tmp/bursty.yaml 2>&1 >/dev/null | tail -1 | python3 -c "
import json, sys
stats = json.loads(sys.stdin.readline())
rate = stats['traces_per_second']
print('rate above 150/s (5x burst):', rate > 150)
"
```

```output
rate above 150/s (5x burst): True
```

## Comparing all four

Running the same topology with each pattern shows how arrival models affect throughput. The `--duration` flag overrides any config-level duration setting.

```bash
python3 -c "
import subprocess, json

patterns = ['uniform', 'diurnal', 'poisson', 'bursty']
results = {}
for p in patterns:
    cfg = f'''services:
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
  pattern: {p}
'''
    with open(f'/tmp/cmp-{p}.yaml', 'w') as f:
        f.write(cfg)
    proc = subprocess.run(
        ['./build/motel-synth', 'run', '--stdout', '--duration', '1s', f'/tmp/cmp-{p}.yaml'],
        capture_output=True, text=True)
    lines = (proc.stdout + proc.stderr).strip().split('\n')
    stats = json.loads(lines[-1])
    results[p] = stats['traces_per_second']

print('bursty > uniform:', results['bursty'] > results['uniform'])
print('diurnal < uniform:', results['diurnal'] < results['uniform'])
print('poisson close to uniform:', abs(results['poisson'] - results['uniform']) < 20)
"
```

```output
bursty > uniform: True
diurnal < uniform: True
poisson close to uniform: True
```

The four patterns cover common load testing scenarios: steady state (uniform), natural traffic cycles (diurnal), realistic arrival randomness (poisson), and sudden traffic spikes (bursty).
