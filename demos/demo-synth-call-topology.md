# motel-synth: Scenario Call Topology Changes

*2026-02-14T12:54:57Z*

Scenarios can modify which downstream services an operation calls during a time window. The `add_calls` and `remove_calls` directives let you model circuit breakers, fallback caches, and dependency changes without separate topology files. This demo walks through a circuit-breaker pattern where database calls are replaced by cache lookups during a degradation window.

## The circuit-breaker topology

This config defines three services. Normally, `api.request` calls `database.query`. At the 3-second mark, a circuit-breaker scenario activates for 4 seconds: it removes the database call and adds a cache fallback.

```bash
cat examples/synth/circuit-breaker.yaml
```

```output
# Topology for demonstrating scenario call topology changes
# Shows add_calls and remove_calls for circuit-breaker and fallback patterns

version: 1

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

  cache:
    operations:
      get:
        duration: 1ms +/- 0.5ms

traffic:
  rate: 50/s

scenarios:
  # Circuit breaker: remove database call, add cache fallback
  - name: circuit breaker
    at: +3s
    duration: 4s
    override:
      api.request:
        remove_calls:
          - database.query
        add_calls:
          - target: cache.get
```

## Validation

The validator checks that `add_calls` targets and `remove_calls` references exist in the topology.

```bash
./build/motel-synth validate examples/synth/circuit-breaker.yaml
```

```output
Configuration valid: 3 services, 2 root operations
```

Invalid targets are caught at validation time. An `add_calls` reference to a non-existent operation is rejected:

```bash
cat > /tmp/bad-add-calls.yaml << 'EOF'
version: 1
services:
  api:
    operations:
      request:
        duration: 10ms
        calls:
          - database.query
  database:
    operations:
      query:
        duration: 5ms
traffic:
  rate: 10/s
scenarios:
  - name: bad target
    at: +1s
    duration: 5s
    override:
      api.request:
        add_calls:
          - target: ghost.lookup
EOF
./build/motel-synth validate /tmp/bad-add-calls.yaml 2>&1 | head -1
```

```output
Error: scenario "bad target": override "api.request": add_calls: target "ghost.lookup" references unknown operation
```

## Baseline: before the scenario

A short 2-second run stays within the pre-scenario window. All `api.request` spans call `database.query` as their downstream dependency.

```bash
./build/motel-synth run --stdout --duration 2s examples/synth/circuit-breaker.yaml 2>/dev/null | jq -r "select(.Parent.SpanID | test(\"^0+$\") | not) | .Name" | sort -u
```

```output
query
```

## With scenario: call topology changes

Running for 8 seconds covers all three phases: before (0-3s), during (3-7s), and after (7-8s) the circuit-breaker window. During the scenario, `api.request` stops calling `database.query` and calls `cache.get` instead. We can observe this by examining which child span names appear under `request` spans in each time window.

```bash
./build/motel-synth run --stdout --duration 8s examples/synth/circuit-breaker.yaml 2>/dev/null | python3 -c '
import json, sys

spans = [json.loads(line) for line in sys.stdin]
by_id = {s["SpanContext"]["SpanID"]: s for s in spans}

def offset_secs(s, base):
    t = s["StartTime"]
    parts = t.split("T")[1].rstrip("Z").split(":")
    return int(parts[0])*3600 + int(parts[1])*60 + float(parts[2]) - base

base_parts = spans[0]["StartTime"].split("T")[1].rstrip("Z").split(":")
base = int(base_parts[0])*3600 + int(base_parts[1])*60 + float(base_parts[2])

windows = {"before (0-3s)": {}, "during (3-7s)": {}, "after (7s+)": {}}
for s in spans:
    pid = s["Parent"]["SpanID"]
    if pid == "0000000000000000":
        continue
    parent = by_id.get(pid)
    if not parent or parent["Name"] != "request":
        continue
    off = offset_secs(s, base)
    w = "before (0-3s)" if off < 3 else ("during (3-7s)" if off < 7 else "after (7s+)")
    name = s["Name"]
    windows[w][name] = windows[w].get(name, 0) + 1

for w in ["before (0-3s)", "during (3-7s)", "after (7s+)"]:
    calls = windows[w]
    has_query = "query" in calls
    has_get = "get" in calls
    print(f"{w}: calls query={has_query}, calls get={has_get}")
'

```

```output
before (0-3s): calls query=True, calls get=False
during (3-7s): calls query=False, calls get=True
after (7s+): calls query=True, calls get=False
```

Before the scenario window, `api.request` calls `database.query` as configured in the base topology. During the circuit-breaker window (3-7s), the database call is removed and replaced by `cache.get`. After the window closes, the original call graph is restored. Each phase shows a clean switch with no overlap — the call topology change is atomic per scenario activation.

## Run statistics

The stats output confirms traces were generated across the full duration with spans from all three services.

```bash
./build/motel-synth run --stdout --duration 8s examples/synth/circuit-breaker.yaml 2>&1 >/dev/null | tail -1 | jq -r "\"has traces: \(.traces > 0)\",\"spans per trace > 1: \(.spans > .traces)\",\"rate ~50/s: \(.traces_per_second > 40)\""
```

```output
has traces: true
spans per trace > 1: true
rate ~50/s: true
```
