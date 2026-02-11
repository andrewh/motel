# motel-synth: Wide Attributes, Call Styles, and Run Statistics

*2026-02-11T14:14:25Z*

motel-synth generates synthetic OTLP traces from a YAML topology definition. This demo walks through three features that address community-reported OTel failure modes: per-operation attributes for wide structured events, parallel/sequential call styles, and structured run statistics.

## The example topology

The example config defines five services with per-operation attributes, weighted status codes, high-cardinality sequence fields, and a parallel call style on order-service.

```bash
cat examples/synth/basic-topology.yaml
```

```output
# Five-service topology demonstrating motel-synth capabilities
# Generates realistic traces with gateway, two backends, and two datastores

services:
  gateway:
    attributes:
      deployment.environment: production
      service.namespace: demo
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        error_rate: 0.1%
        attributes:
          http.request.method:
            value: GET
          http.route:
            value: "/api/v1/users"
          http.response.status_code:
            values:
              "200": 95
              "404": 3
              "500": 2
          user.id:
            sequence: "user-{n}"
        calls:
          - user-service.list
      POST /orders:
        duration: 80ms +/- 20ms
        error_rate: 0.5%
        attributes:
          http.request.method:
            value: POST
          http.route:
            value: "/api/v1/orders"
          http.response.status_code:
            values:
              "201": 90
              "400": 5
              "500": 5
        calls:
          - order-service.create

  user-service:
    attributes:
      deployment.environment: production
    operations:
      list:
        duration: 20ms +/- 5ms
        error_rate: 0.1%
        calls:
          - postgres.query

  order-service:
    attributes:
      deployment.environment: production
    operations:
      create:
        duration: 50ms +/- 15ms
        error_rate: 0.5%
        call_style: parallel
        calls:
          - postgres.query
          - redis.get

  postgres:
    attributes:
      db.system: postgresql
    operations:
      query:
        duration: 5ms +/- 2ms
        error_rate: 0.01%
        attributes:
          db.operation:
            values:
              SELECT: 70
              INSERT: 20
              UPDATE: 10

  redis:
    attributes:
      db.system: redis
    operations:
      get:
        duration: 1ms +/- 0.5ms
        error_rate: 0.001%
        attributes:
          db.operation:
            value: GET

traffic:
  rate: 100/s

scenarios:
  - name: database degradation
    at: +5m
    duration: 10m
    override:
      postgres.query:
        duration: 500ms +/- 100ms
        error_rate: 15%
```

## Validation

The validate command checks the topology for structural correctness, including the new attribute definitions and call style fields.

```bash
./build/motel-synth validate examples/synth/basic-topology.yaml
```

```output
Configuration valid: 5 services, 2 root operations
```

## Wide attributes on spans

Running with `--stdout` emits spans as JSON. Each gateway span carries per-operation attributes: static values (`http.route`), weighted random values (`http.response.status_code`), and high-cardinality sequences (`user.id`). This demonstrates why arbitrarily-wide structured events are more powerful than low-cardinality metrics.

```bash
./build/motel-synth run --stdout --duration 200ms examples/synth/basic-topology.yaml 2>/dev/null | grep 'GET /users' | head -1 | python3 -c "
import json, sys
span = json.loads(sys.stdin.readline())
for attr in span['Attributes']:
    print(f\"  {attr['Key']}: {attr['Value']['Value']}\")
"
```

```output
  synth.service: gateway
  synth.operation: GET /users
  deployment.environment: production
  service.namespace: demo
  http.request.method: GET
  http.route: /api/v1/users
  http.response.status_code: 200
  user.id: user-1
```

The gateway span carries service attributes (`deployment.environment`, `service.namespace`) alongside operation-specific attributes. The `user.id` field increments per trace, producing high-cardinality data that would be impossible to represent with metrics alone.

## Parallel vs sequential call styles

The order-service uses `call_style: parallel`, so its two downstream calls (postgres and redis) start at the same time. We can verify this by checking whether the start times of sibling spans match.

```bash
./build/motel-synth run --stdout --duration 200ms examples/synth/basic-topology.yaml 2>/dev/null | python3 -c "
import json, sys
spans = [json.loads(line) for line in sys.stdin]
traces = {}
for s in spans:
    traces.setdefault(s['SpanContext']['TraceID'], []).append(s)
for group in traces.values():
    names = {s['Name'] for s in group}
    if 'create' in names and 'query' in names and 'get' in names:
        query = next(s for s in group if s['Name'] == 'query')
        get = next(s for s in group if s['Name'] == 'get')
        print('parallel (query and get share start time):', query['StartTime'] == get['StartTime'])
        break
"
```

```output
parallel (query and get share start time): True
```

## Run statistics

motel-synth emits structured JSON to stderr at the end of a run. This addresses the silent-failure critique: if your observability pipeline drops data, compare these numbers against what arrived.

```bash
./build/motel-synth run --stdout --duration 1s examples/synth/basic-topology.yaml 2>&1 >/dev/null | python3 -c "
import json, sys
stats = json.loads(sys.stdin.readline())
print('fields:', sorted(stats.keys()))
print('traces > 0:', stats['traces'] > 0)
print('spans > traces:', stats['spans'] > stats['traces'])
print('has rates:', stats['traces_per_second'] > 0 and stats['spans_per_second'] > 0)
"
```

```output
fields: ['elapsed_ms', 'error_rate', 'errors', 'spans', 'spans_per_second', 'traces', 'traces_per_second']
traces > 0: True
spans > traces: True
has rates: True
```
