# Use motel with otel-cli

[otel-cli](https://github.com/equinix-labs/otel-cli) is a command-line tool for creating OpenTelemetry spans from shell scripts. motel generates synthetic telemetry from topology definitions. The two tools complement each other — otel-cli instruments real commands, motel simulates entire distributed systems.

This guide covers practical ways to use them together.

## Prerequisites

- motel installed
- otel-cli installed ([releases](https://github.com/equinix-labs/otel-cli/releases))

Save this as `topology.yaml` to use with the examples below:

```yaml
version: 1

services:
  web-gateway:
    operations:
      GET /products:
        duration: 50ms +/- 15ms
        calls:
          - product-service.list

  product-service:
    operations:
      list:
        duration: 20ms +/- 5ms
        calls:
          - database.query

  database:
    operations:
      query:
        duration: 5ms +/- 2ms

traffic:
  rate: 1/s
```

## Use otel-cli's TUI as a trace viewer

otel-cli can run as a local OTLP server with a terminal UI that displays incoming traces. This gives you a zero-setup way to visually inspect what motel generates — no collector, no Jaeger, no Grafana needed.

In one terminal, start the TUI server:

```sh
otel-cli server tui
```

This listens for gRPC on `localhost:4317`. In another terminal, point motel at it:

```sh
motel run --endpoint localhost:4317 --protocol grpc --duration 10s topology.yaml
```

The TUI displays each span as it arrives. This is particularly useful when you are authoring a new topology and want to see whether the call graph, durations, and error rates look right before sending traffic to a real backend.

### Tips for the TUI workflow

- **Keep the rate low.** The TUI is meant for inspection, not throughput. A rate of `5/s` or `10/s` is plenty.
- **Use a short duration.** A few seconds of traffic gives you enough spans to check the shape without flooding the display.
- **Watch for errors.** Spans with error status stand out in the TUI, making it easy to verify that your error rates and cascading failures behave as expected.

## Mix real and synthetic traces

You can use otel-cli to instrument a real shell command alongside motel's synthetic traffic, with otel-cli's TUI server as the receiver.

In the first terminal, start the TUI server:

```sh
otel-cli server tui
```

In the second terminal, start motel generating background traffic. Use a topology with a low traffic rate (e.g. `rate: 1/s`) so the TUI stays readable:

```sh
motel run --endpoint localhost:4317 --protocol grpc --duration 5m topology.yaml
```

In a third terminal, use otel-cli to instrument a real command. The distinct service name `my-deploy-script` makes it easy to spot among motel's synthetic spans:

```sh
otel-cli exec \
  --service my-deploy-script \
  --name "curl homepage" \
  --endpoint localhost:4317 \
  -- curl -sS https://example.com -o /dev/null
```

Look for the `my-deploy-script` span in the TUI — it appears alongside motel's synthetic spans from your topology.

## Limitations

**otel-cli's JSON server output is not compatible with motel import.** The `otel-cli server json` command writes individual protobuf-marshaled spans in a directory tree (`{traceId}/{spanId}/span.json`). motel's `import` command expects either stdouttrace format (one JSON span per line) or OTLP JSON (`resourceSpans` arrays). You cannot pipe otel-cli's JSON output directly into `motel import`.

If you want to capture real traces and import them into motel, use a collector with a [file exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/fileexporter) configured to write OTLP JSON, then feed that output to `motel import`.

## Further reading

- [otel-cli documentation](https://github.com/equinix-labs/otel-cli)
- [Model your services](model-your-services.md) — creating topology files
- [Validate a collector pipeline](validate-collector-pipeline.md) — testing collector configurations with motel
