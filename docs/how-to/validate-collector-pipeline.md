# Validate a Collector Pipeline

This guide covers using motel to verify that an OpenTelemetry Collector accepts, processes, and forwards telemetry correctly. It works for any collector configuration — whether you are setting up a new pipeline, debugging a broken one, or testing changes before deployment.

## Prerequisites

- motel installed
- A topology file (even a minimal one works — see [Model your services](model-your-services.md))
- An OpenTelemetry Collector running and reachable from your machine

## 1. Establish a baseline with stdout

Before involving the network, confirm that motel generates the traces you expect:

```sh
motel run --stdout --duration 5s topology.yaml | head -20
```

If this produces JSON spans, motel and your topology are working. Any problems you encounter later are between motel and the collector.

## 2. Point motel at your collector

Send traffic to the collector's OTLP receiver:

```sh
motel run --endpoint localhost:4318 --duration 10s topology.yaml
```

By default motel uses HTTP/protobuf on port 4318. If your collector listens on a different port or protocol, adjust accordingly:

```sh
# gRPC (typically port 4317)
motel run --endpoint localhost:4317 --protocol grpc --duration 10s topology.yaml

# HTTP/protobuf on a non-standard port
motel run --endpoint localhost:9090 --protocol http/protobuf --duration 10s topology.yaml
```

A clean run with no errors means the collector accepted every export request.

## 3. Isolate connection problems

When motel reports errors, narrow down the cause by switching one variable at a time.

### Protocol mismatch

If you see errors like `405 Method Not Allowed` or unexpected EOF, you may be sending gRPC to an HTTP receiver or vice versa. Try the other protocol:

```sh
# If http/protobuf fails, try gRPC
motel run --endpoint localhost:4317 --protocol grpc --duration 5s topology.yaml

# If gRPC fails, try http/protobuf
motel run --endpoint localhost:4318 --protocol http/protobuf --duration 5s topology.yaml
```

### TLS errors

Errors mentioning `tls: first record does not look like a TLS handshake` or `certificate signed by unknown authority` indicate a TLS mismatch. Check whether your collector expects TLS and whether the endpoint URL matches.

### Connection refused or timeout

`DEADLINE_EXCEEDED` or `connection refused` means motel cannot reach the collector at all. Verify:

- The collector process is running
- The port is correct and not blocked by a firewall
- The hostname resolves from the machine running motel

A quick connectivity check:

```sh
curl -v http://localhost:4318/v1/traces
```

You should get a response (even an error like 405 or 400) rather than a connection failure.

## 4. Verify end-to-end pipeline flow

Confirming that the collector accepts traffic is only half the picture. You also need to verify that spans reach your backend.

### Step 1: Send a short, identifiable burst

Use a minimal topology with a distinctive service name so spans are easy to find:

```yaml
version: 1

services:
  pipeline-test:
    operations:
      validate:
        duration: 10ms

traffic:
  rate: 5/s
```

```sh
motel run --endpoint localhost:4318 --duration 10s pipeline-test.yaml
```

### Step 2: Check your backend

Search your tracing backend for spans from the `pipeline-test` service within the last minute. If they appear, your full pipeline — motel to collector to backend — is working.

If spans do not appear:

- Check the collector's own logs for export errors
- Verify the collector's exporter configuration (endpoint, authentication, TLS)
- Add a `debug` exporter to the collector pipeline temporarily to confirm spans arrive at the collector

### Step 3: Test other signals

If your pipeline carries metrics or logs as well as traces, verify those separately:

```sh
# Metrics only
motel run --endpoint localhost:4318 --signals metrics --duration 10s topology.yaml

# All signals
motel run --endpoint localhost:4318 --signals traces,metrics,logs --duration 10s topology.yaml
```

## Common failure modes

| Symptom | Likely cause | What to try |
|---|---|---|
| `connection refused` | Collector not running or wrong port | Check collector process and port |
| `DEADLINE_EXCEEDED` | Network timeout, firewall, or DNS | Verify connectivity with curl |
| `405 Method Not Allowed` | Protocol mismatch (gRPC vs HTTP) | Switch `--protocol` |
| `certificate signed by unknown authority` | TLS certificate not trusted | Check TLS configuration |
| `tls: first record does not look like a TLS handshake` | Sending TLS to a non-TLS endpoint | Use the correct scheme |
| Motel succeeds but spans missing in backend | Collector pipeline misconfigured | Check collector logs and exporter config |

## Further reading

- [Model your services](model-your-services.md) — creating topology files
- [CLI reference](../reference/synth.md) — full list of flags and options
