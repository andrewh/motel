# Visualise Traces

motel generates OTLP traces, but you need a backend to actually see them. This guide covers four options, from zero-setup terminal viewing to hosted platforms.

All examples use the [basic topology](../examples/basic-topology.yaml) — a five-service setup with a gateway, two backends, and two datastores. If you have the source tree, copy it locally:

```sh
cp docs/examples/basic-topology.yaml my-topology.yaml
```

Or fetch it directly from GitHub (motel accepts URLs anywhere a file path is accepted):

```sh
motel validate https://raw.githubusercontent.com/andrewh/motel/main/docs/examples/basic-topology.yaml
```

## otel-cli (terminal, zero setup)

The quickest way to see traces is [otel-cli's TUI server](https://github.com/equinix-labs/otel-cli), which displays spans in your terminal with no external dependencies.

In one terminal:

```sh
otel-cli server tui
```

In another:

```sh
motel run --endpoint localhost:4317 --protocol grpc --duration 5s my-topology.yaml
```

The TUI shows each span as it arrives — service name, operation, duration, and error status. Good for checking that a topology looks right before sending traffic elsewhere.

See [Use motel with otel-cli](use-with-otel-cli.md) for more detail.

## Jaeger (Docker, self-hosted)

[Jaeger](https://www.jaegertracing.io/) is an open-source distributed tracing backend. Its all-in-one Docker image includes a collector, in-memory storage, and a web UI.

### Prerequisites

- Docker or [Apple Container](https://github.com/apple/container) (macOS 26+, Apple silicon)

### Start Jaeger

With Docker:

```sh
docker run --rm -d --name jaeger \
  -p 4317:4317 \
  -p 4318:4318 \
  -p 16686:16686 \
  jaegertracing/all-in-one:latest
```

With Apple Container:

```sh
container run --rm -d --name jaeger \
  -p 4317:4317 \
  -p 4318:4318 \
  -p 16686:16686 \
  jaegertracing/all-in-one:latest
```

This exposes:
- `4317` — OTLP gRPC receiver
- `4318` — OTLP HTTP receiver
- `16686` — Jaeger UI

### Send traces

```sh
motel run --endpoint localhost:4318 --protocol http/protobuf \
  --duration 10s my-topology.yaml
```

### Inspect results

Open <http://localhost:16686> in your browser. Select the `gateway` service from the dropdown and click **Find Traces**. Click a trace to see the waterfall view showing calls fanning out from `gateway` through the backend services to the datastores.

![Jaeger trace waterfall](images/jaeger-trace-waterfall.png)

Things to check:

- All five services appear in the service dropdown
- `GET /users` traces show `gateway` → `user-service` → `postgres`
- `POST /orders` traces show `gateway` → `order-service` → `postgres` + `redis` in parallel
- Span durations fall within the configured ranges (e.g. 30ms +/- 10ms for `GET /users`)

### Clean up

```sh
docker stop jaeger
```

Or with Apple Container:

```sh
container stop jaeger
```

## Grafana + Tempo (self-hosted)

[Grafana Tempo](https://grafana.com/oss/tempo/) is a trace backend that integrates with Grafana's Explore view. This setup runs Tempo and Grafana together so you can send traces to Tempo and visualise them in Grafana.

### Prerequisites

- Docker (with Docker Compose) or [Apple Container](https://github.com/apple/container) (macOS 26+, Apple silicon)

### Create the configuration

Create a directory for the project:

```sh
mkdir motel-tempo && cd motel-tempo
```

Create `tempo.yaml`:

```yaml
stream_over_http_enabled: true

server:
  http_listen_port: 3200

distributor:
  receivers:
    otlp:
      protocols:
        http:
          endpoint: 0.0.0.0:4318
        grpc:
          endpoint: 0.0.0.0:4317

storage:
  trace:
    backend: local
    local:
      path: /var/tempo/traces
    wal:
      path: /var/tempo/wal
```

Create `grafana-datasources.yaml`:

```yaml
apiVersion: 1

datasources:
  - name: Tempo
    type: tempo
    access: proxy
    url: http://tempo:3200
    isDefault: true
```

### Start the stack with Docker Compose

Create `docker-compose.yaml`:

```yaml
services:
  tempo:
    image: grafana/tempo:latest
    command: ["-config.file=/etc/tempo.yaml"]
    volumes:
      - ./tempo.yaml:/etc/tempo.yaml:ro
    ports:
      - "4317:4317"
      - "4318:4318"
      - "3200:3200"

  grafana:
    image: grafana/grafana:latest
    environment:
      GF_AUTH_ANONYMOUS_ENABLED: "true"
      GF_AUTH_ANONYMOUS_ORG_ROLE: Admin
    ports:
      - "3000:3000"
    volumes:
      - ./grafana-datasources.yaml:/etc/grafana/provisioning/datasources/datasources.yaml:ro
```

```sh
docker compose up -d
```

### Start the stack with Apple Container

Apple Container does not have a compose equivalent, so start the containers individually on a shared network:

```sh
container network create motel-tempo

container run --rm -d --name tempo \
  --network motel-tempo \
  -v ./tempo.yaml:/etc/tempo.yaml:ro \
  -p 4317:4317 \
  -p 4318:4318 \
  -p 3200:3200 \
  grafana/tempo:latest \
  -config.file=/etc/tempo.yaml

container run --rm -d --name grafana \
  --network motel-tempo \
  -e GF_AUTH_ANONYMOUS_ENABLED=true \
  -e GF_AUTH_ANONYMOUS_ORG_ROLE=Admin \
  -v ./grafana-datasources.yaml:/etc/grafana/provisioning/datasources/datasources.yaml:ro \
  -p 3000:3000 \
  grafana/grafana:latest
```

The shared `motel-tempo` network lets Grafana reach Tempo by container name (`http://tempo:3200`), matching the datasource configuration.

Wait a few seconds for Grafana and Tempo to start.

### Send traces

```sh
motel run --endpoint localhost:4318 --protocol http/protobuf \
  --duration 10s my-topology.yaml
```

### Inspect results

Open <http://localhost:3000/explore> in your browser. Select **Tempo** as the data source, switch to the **Search** tab, and choose `gateway` from the service name dropdown. Run the query to see a list of traces. Click a trace to open the waterfall panel.

![Grafana Tempo trace panel](images/grafana-tempo-trace-panel.png)

### Clean up

With Docker Compose:

```sh
docker compose down
cd .. && rm -rf motel-tempo
```

With Apple Container:

```sh
container stop tempo grafana
container network rm motel-tempo
cd .. && rm -rf motel-tempo
```

## Hosted backends

motel works with any OTLP-compatible backend — no motel-specific setup is needed. Point `--endpoint` at the backend's OTLP ingestion URL, set authentication headers via the standard `OTEL_EXPORTER_OTLP_HEADERS` environment variable, and traces appear in the backend's UI.

```sh
OTEL_EXPORTER_OTLP_HEADERS='x-api-key=YOUR_KEY' \
  motel run --endpoint https://api.example.com:4318 \
  --protocol http/protobuf \
  --duration 10s my-topology.yaml
```

| Backend | OTLP ingestion docs |
|---|---|
| Honeycomb | [Honeycomb OTLP docs](https://docs.honeycomb.io/send-data/opentelemetry/) |
| Datadog | [Datadog OTLP docs](https://docs.datadoghq.com/opentelemetry/interoperability/otlp_in_datadog_agent/) |
| Grafana Cloud | [Grafana Cloud OTLP docs](https://grafana.com/docs/grafana-cloud/send-data/otlp/send-data-otlp/) |
| Dynatrace | [Dynatrace OTLP docs](https://docs.dynatrace.com/docs/extend-dynatrace/opentelemetry/getting-started/otlp-export) |
| New Relic | [New Relic OTLP docs](https://docs.newrelic.com/docs/opentelemetry/best-practices/opentelemetry-otlp/) |

Each backend has its own authentication mechanism (API keys, tokens, headers). Check the linked docs for the specific headers and endpoint format required.

## Further reading

- [Use motel with otel-cli](use-with-otel-cli.md) — terminal trace viewing in detail
- [Test backend integrations](test-backend-integration.md) — verifying backends accept and display traces correctly
- [Model your services](model-your-services.md) — creating topology files
