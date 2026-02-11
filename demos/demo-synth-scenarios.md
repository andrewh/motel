# motel-synth: Scenario Overrides

*2026-02-11T16:00:00Z*

Scenarios let you inject time-windowed behaviour changes into a running simulation. A scenario overrides operation parameters (duration, error rate) during a specific window, then reverts to baseline. This demo walks through configuration, validation, and observable impact of a database degradation scenario.

## The scenario topology

This config runs two services at 50/s. At the 2-second mark, a degradation scenario activates for 3 seconds: database.query latency jumps from 5ms to 200ms and error rate spikes from 0.1% to 25%.

```bash
cat examples/synth/scenario-override.yaml
```

```output
# Topology for demonstrating scenario overrides
# Short scenario windows so the demo runs quickly

services:
  api:
    operations:
      request:
        duration: 10ms +/- 3ms
        error_rate: 0.5%
        calls:
          - database.query

  database:
    operations:
      query:
        duration: 5ms +/- 2ms
        error_rate: 0.1%

traffic:
  rate: 50/s

scenarios:
  - name: database degradation
    at: +2s
    duration: 3s
    override:
      database.query:
        duration: 200ms +/- 50ms
        error_rate: 25%
```

## Validation

The validator checks scenario timing, override references, and duration formats.

```bash
./build/motel-synth validate examples/synth/scenario-override.yaml
```

```output
Configuration valid: 2 services, 1 root operations
```

Invalid scenario references are caught. For example, overriding a non-existent operation:

```bash
cat > /tmp/bad-scenario.yaml << 'EOF'
services:
  api:
    operations:
      request:
        duration: 10ms
traffic:
  rate: 10/s
scenarios:
  - name: test
    at: +1m
    duration: 5m
    override:
      ghost.op:
        duration: 100ms
EOF
./build/motel-synth validate /tmp/bad-scenario.yaml 2>&1 | head -1
```

```output
Error: scenario "test": override "ghost.op" references unknown operation
```

## Baseline: no scenario

First, a 1-second run without scenarios. With combined error rates of 0.5% (api) and 0.1% (database), errors are rare in a short run.

```bash
cat > /tmp/no-scenario.yaml << 'EOF'
services:
  api:
    operations:
      request:
        duration: 10ms +/- 3ms
        error_rate: 0.5%
        calls:
          - database.query
  database:
    operations:
      query:
        duration: 5ms +/- 2ms
        error_rate: 0.1%
traffic:
  rate: 50/s
EOF
./build/motel-synth run --stdout --duration 1s /tmp/no-scenario.yaml 2>&1 >/dev/null | tail -1 | python3 -c "
import json, sys
stats = json.loads(sys.stdin.readline())
print('baseline error_rate < 5%:', stats['error_rate'] < 0.05)
"
```

```output
baseline error_rate < 5%: True
```

## With scenario: elevated errors

Running for 6 seconds covers before (0-2s), during (2-5s), and after (5-6s) the degradation window. The 25% error rate on database.query during the scenario drives the overall error rate well above baseline.

```bash
./build/motel-synth run --stdout --duration 6s examples/synth/scenario-override.yaml 2>&1 >/dev/null | tail -1 | python3 -c "
import json, sys
stats = json.loads(sys.stdin.readline())
print('scenario error_rate > 2%:', stats['error_rate'] > 0.02)
print('scenario has errors:', stats['errors'] > 0)
"
```

```output
scenario error_rate > 2%: True
scenario has errors: True
```

## Latency impact

The scenario also inflates database.query duration from 5ms to 200ms. Examining individual span durations shows two distinct populations: fast spans from outside the window and slow spans from during the degradation.

```bash
./build/motel-synth run --stdout --duration 6s examples/synth/scenario-override.yaml 2>/dev/null | python3 -c "
import json, sys
from datetime import datetime

spans = [json.loads(line) for line in sys.stdin]
durations = []
for s in spans:
    if s['Name'] == 'query':
        start = datetime.fromisoformat(s['StartTime'].rstrip('Z'))
        end = datetime.fromisoformat(s['EndTime'].rstrip('Z'))
        dur_ms = (end - start).total_seconds() * 1000
        durations.append(dur_ms)

fast = [d for d in durations if d < 50]
slow = [d for d in durations if d >= 50]
print('has fast spans (<50ms):', len(fast) > 0)
print('has slow spans (>=50ms):', len(slow) > 0)
print('bimodal distribution:', len(fast) > 0 and len(slow) > 0)
"
```

```output
has fast spans (<50ms): True
has slow spans (>=50ms): True
bimodal distribution: True
```

The fast population (~5ms) corresponds to normal operation; the slow population (~200ms) corresponds to the degradation window. This bimodal latency distribution is exactly what you would see during a real database incident, making it useful for testing alerting thresholds and SLO breach detection.
