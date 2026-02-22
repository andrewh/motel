# Test Backend Integrations

This guide covers using motel to verify that an OTLP-compatible backend accepts, stores, and displays traces correctly. The same approach works for initial setup, multi-backend routing, and backend migrations.

## Prerequisites

- motel installed
- A topology file (see [Model your services](model-your-services.md) to create one)
- One or more OTLP-compatible backends running and reachable

## Verify a new backend

Send a short burst of traces to confirm the backend accepts OTLP data and renders it correctly.

### 1. Create a test topology

Use a small topology that exercises the features you care about — multiple services, varying durations, and some errors:

```yaml
version: 1

services:
  web-gateway:
    attributes:
      deployment.environment: staging
      service.version: 1.0.0
    operations:
      GET /healthz:
        duration: 5ms +/- 2ms
        calls:
          - api-server.healthcheck

  api-server:
    attributes:
      deployment.environment: staging
      service.version: 2.3.1
    operations:
      healthcheck:
        duration: 2ms +/- 1ms
      process-order:
        duration: 80ms +/- 20ms
        error_rate: 5%
        calls:
          - database.query

  database:
    attributes:
      deployment.environment: staging
      db.system: postgresql
    operations:
      query:
        duration: 10ms +/- 3ms
        error_rate: 1%

traffic:
  rate: 10/s
```

Save this as `backend-test.yaml`.

### 2. Send traces to the backend

Point motel at your backend's OTLP endpoint:

```sh
motel run --endpoint http://localhost:4318 --protocol http/protobuf \
  --duration 10s backend-test.yaml
```

For gRPC endpoints:

```sh
motel run --endpoint localhost:4317 --protocol grpc \
  --duration 10s backend-test.yaml
```

### 3. Check the results

Open your backend's UI and verify:

- All three services appear (web-gateway, api-server, database)
- Traces show the expected call hierarchy: web-gateway calls api-server, which calls database
- Span durations are in the expected ranges
- Some spans on api-server.process-order and database.query are marked as errors
- Resource attributes (`deployment.environment`, `service.version`, `db.system`) are visible

If traces do not appear, check that the endpoint URL and protocol match your backend's configuration. Use `--stdout` to confirm motel is generating valid data:

```sh
motel run --stdout --duration 2s backend-test.yaml | head -5
```

## Test multi-backend routing

A common pattern is routing traces to different backends based on content — for example, sending error traces to a dedicated backend for alerting. motel does not route traces itself, but it generates consistent traffic that you can use to verify your collector's routing rules.

### 1. Configure your collector

Set up an OpenTelemetry Collector with routing logic. For example, a collector config that sends all traces to a primary backend and only error traces to a second backend:

```yaml
receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318

exporters:
  otlphttp/primary:
    endpoint: http://primary-backend:4318
  otlphttp/errors:
    endpoint: http://errors-backend:4318

processors:
  filter/errors-only:
    error_mode: ignore
    traces:
      span:
        - 'status.code != STATUS_CODE_ERROR'

service:
  pipelines:
    traces/all:
      receivers: [otlp]
      exporters: [otlphttp/primary]
    traces/errors:
      receivers: [otlp]
      processors: [filter/errors-only]
      exporters: [otlphttp/errors]
```

Both pipelines receive all traffic from the same OTLP receiver. The `filter/errors-only` processor drops non-error spans, so only error spans reach the errors backend.

### 2. Send traffic through the collector

Point motel at the collector's intake:

```sh
motel run --endpoint http://localhost:4318 --protocol http/protobuf \
  --duration 30s backend-test.yaml
```

Use a topology with a meaningful error rate (the example above uses 5% on `process-order`) so that both backends receive data.

### 3. Verify routing

- **Primary backend**: should contain all traces
- **Errors backend**: should contain only traces with error spans

Check that error spans in the errors backend carry the same trace IDs, attributes, and timing as their counterparts in the primary backend.

## Verify attribute handling

Different backends handle attributes differently — some index specific keys, some have length limits, some drop unknown attribute types. Use motel to send traces with a range of attribute shapes and verify they survive the round trip.

### 1. Add varied attributes to your topology

```yaml
services:
  attribute-test:
    attributes:
      deployment.environment: production
      service.version: 3.1.4
      cloud.provider: aws
      cloud.region: eu-west-1
    operations:
      varied-attributes:
        duration: 20ms +/- 5ms
        attributes:
          http.request.method:
            value: GET
          http.response.status_code:
            range: [200, 599]
          http.route:
            values: {"/api/users": 50, "/api/orders": 30, "/api/products": 20}
          request.id:
            sequence: "req-{n}"

traffic:
  rate: 5/s
```

### 2. Send and inspect

```sh
motel run --endpoint http://localhost:4318 --protocol http/protobuf \
  --duration 10s attribute-test.yaml
```

In your backend, verify:

- Resource attributes appear at the service level (`deployment.environment`, `cloud.provider`)
- Span attributes appear on individual spans (`http.request.method`, `http.response.status_code`)
- Numeric ranges produce varied integer values, not strings
- Weighted values produce the expected distribution (roughly 50/30/20 across routes)
- Sequence values increment correctly (`req-1`, `req-2`, ...)

## Smoke test a backend migration

When migrating from one backend to another, use motel to send identical traffic to both and compare the results side by side.

### 1. Send the same traffic to both backends

Run motel twice with the same topology and duration — once against each backend:

```sh
motel run --endpoint http://old-backend:4318 --protocol http/protobuf \
  --duration 30s backend-test.yaml

motel run --endpoint http://new-backend:4318 --protocol http/protobuf \
  --duration 30s backend-test.yaml
```

### 2. Compare

Check both backends for:

- Same number of services and operations visible
- Consistent attribute rendering (no truncation, no missing keys)
- Similar trace visualisation (waterfall view, service maps)
- Error spans displayed and filterable

The trace IDs will differ between runs, but the structure, timing distributions, and attribute values should be consistent. What matters is that both backends handle the same shape of data identically.

### 3. Use scenarios to stress-test

Add a scenario to simulate a latency spike and verify both backends handle it:

```yaml
scenarios:
  - name: database latency spike
    at: +5s
    duration: 10s
    override:
      database.query:
        duration: 500ms +/- 100ms
        error_rate: 15%
```

Run with the scenario and confirm both backends display the spike correctly in their dashboards and alerting views.

## Further reading

- [Model your services](model-your-services.md) — creating and refining topologies
- [CLI reference](../reference/synth.md) — all CLI flags and output formats
