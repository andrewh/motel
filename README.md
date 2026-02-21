# motel

[![CI](https://github.com/andrewh/motel/actions/workflows/ci.yml/badge.svg)](https://github.com/andrewh/motel/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/andrewh/motel)](https://goreportcard.com/report/github.com/andrewh/motel)
[![Go Reference](https://pkg.go.dev/badge/github.com/andrewh/motel.svg)](https://pkg.go.dev/github.com/andrewh/motel)

> **motel** /mōˈtel/ _noun_
> mock opentelemetry. A synthetic signal generator for testing
> and developing observability pipelines.

`motel` is a synthetic [OpenTelemetry](https://opentelemetry.io/) generator.

Describe your distributed system in YAML and motel generates realistic traces,
metrics, and logs — no live services required.

## Install

```sh
brew tap andrewh/tap
brew install motel
```

Or with Go:

```sh
go install github.com/andrewh/motel/cmd/motel@latest
```

Or download a binary from the [releases page](https://github.com/andrewh/motel/releases).

## Quick start

```yaml
# my-topology.yaml
version: 1

services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        error_rate: 1%
        calls:
          - users.list
  users:
    operations:
      list:
        duration: 15ms +/- 5ms

traffic:
  rate: 10/s
```

```sh
# Validate the topology
motel validate my-topology.yaml

# Generate traces to stdout
motel run --stdout --duration 5s my-topology.yaml

# Send to an OTLP collector
motel run --endpoint localhost:4318 --duration 30s my-topology.yaml
```

## What it does

motel reads a YAML topology file describing services, operations, call
patterns, latency distributions, and error rates. It walks the topology tree
once per trace, producing spans that look like they came from real instrumented
services. Every span carries `synth.service` and `synth.operation` attributes,
and all signals include a `motel.version` resource attribute, so synthetic
traffic is never mistaken for real data.

Use cases:

- **Test observability pipelines** — feed realistic traces into collectors,
  backends, or dashboards without deploying services
- **Load test** — generate trace traffic at controlled rates with configurable
  patterns (uniform, diurnal, bursty, poisson, custom)
- **Demo and prototype** — show what your system's telemetry will look like
  before building it
- **Import real traces** — `motel import` infers a topology from existing trace
  data, so you can replay and modify production patterns

## Signals

By default motel emits traces. Use `--signals` to add metrics and logs:

```sh
motel run --stdout --signals traces,metrics,logs --slow-threshold 200ms topology.yaml
```

All three signal types are driven by the same topology.

## Documentation

- [Getting started tutorial](docs/tutorials/getting-started.md)
- [CLI reference](docs/reference/synth.md)
- [DSL reference](cmd/motel/README.md) — full topology schema
- [Example topologies](docs/examples/)
- [Modelling your services](docs/how-to/model-your-services.md)
- [How import infers a topology](docs/explanation/import-pipeline/README.md)
- [How motel uses OTel Weaver](docs/explanation/motel-and-weaver.md)

## Licence

[Apache 2.0](LICENSE)
