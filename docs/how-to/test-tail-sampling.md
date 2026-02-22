# Test Tail Sampling Policies

This guide shows how to use motel to generate traces that exercise tail sampling policies in an OpenTelemetry Collector, so you can verify your sampling rules before deploying them against production traffic.

## What you need

- motel installed
- An OpenTelemetry Collector binary (the [contrib distribution](https://github.com/open-telemetry/opentelemetry-collector-contrib) includes the tail sampling processor)

## 1. Create a topology with varied trace characteristics

Tail sampling decisions depend on trace properties: duration, error status, attributes. To test policies effectively, your topology should produce a predictable mix of these characteristics.

The example topology at [`docs/examples/tail-sampling-test.yaml`](../examples/tail-sampling-test.yaml) generates four categories of traces:

- **Normal traces** (majority) -- fast, successful requests through a six-service call graph
- **Error traces** -- payment failures and database errors at low but measurable rates
- **Slow traces** -- scenario-driven latency spikes in database and payment services
- **VIP traces** -- a `customer.tier: vip` attribute on 10-15% of requests, useful for attribute-based sampling

The topology also includes two scenarios that create time windows of degraded behaviour, giving you both steady-state and incident conditions to sample against.

## 2. Configure the collector with tail sampling

Create a collector configuration that receives OTLP traces from motel and applies tail sampling policies. Save this as `collector-config.yaml`:

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  tail_sampling:
    decision_wait: 10s
    num_traces: 1000
    policies:
      # Keep all traces with errors
      - name: errors
        type: status_code
        status_code:
          status_codes:
            - ERROR

      # Keep traces slower than 500ms
      - name: slow-traces
        type: latency
        latency:
          threshold_ms: 500

      # Keep all VIP customer traces
      - name: vip-customers
        type: string_attribute
        string_attribute:
          key: customer.tier
          values:
            - vip

      # Sample 5% of remaining traces
      - name: baseline
        type: probabilistic
        probabilistic:
          sampling_percentage: 5

exporters:
  debug:
    verbosity: detailed

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [tail_sampling]
      exporters: [debug]
```

This configuration applies four policies in order. A trace is kept if **any** policy matches -- errors, slow traces, and VIP traces are always kept, and 5% of everything else is sampled.

## 3. Run motel against the collector

Start the collector:

```sh
otelcol-contrib --config collector-config.yaml
```

In a separate terminal, run motel against it:

```sh
motel run --endpoint localhost:4317 --protocol grpc \
  --duration 15s docs/examples/tail-sampling-test.yaml
```

The 15-second duration covers both scenarios in the topology (slow database at +3s and payment errors at +8s), so you will see traces that match the latency and error policies.

## 4. Verify what gets sampled

The debug exporter logs every trace that passes the sampling filter. Look at the collector output and check:

- **Error traces appear.** Search for spans with `status.code: Error`. These should be present even at low error rates.
- **Slow traces appear.** Look for traces with root span durations above 500ms. These should cluster around the scenario windows.
- **VIP traces appear.** Search for `customer.tier: vip`. Roughly 10-15% of the original traffic should match.
- **Normal traces are sparse.** Fast, successful, non-VIP traces should appear at roughly 5% of their original rate.

For a quick count, pipe motel's stdout output through `jq` to see the raw distribution before sampling:

```sh
motel run --stdout --duration 15s docs/examples/tail-sampling-test.yaml | \
  jq -r 'select(.Parent.SpanID == "0000000000000000") | .Status.Code // "Unset"' | \
  sort | uniq -c
```

Compare this against the collector's debug output to confirm the sampling ratios match your expectations.

## 5. Adjust the topology for edge cases

Once baseline policies work, modify the topology to test boundary conditions.

### What if all traces are slow?

Override the root operation's duration to push every trace above the latency threshold:

```yaml
scenarios:
  - name: everything slow
    at: +0s
    duration: 30s
    override:
      api-gateway.GET /search:
        duration: 1000ms +/- 200ms
      api-gateway.POST /checkout:
        duration: 1500ms +/- 300ms
```

With this override, the latency policy keeps 100% of traces. This tests whether your collector handles the load when tail sampling stops reducing volume.

### What if error rates spike?

Raise the error rate across all services to simulate a widespread outage:

```yaml
scenarios:
  - name: mass errors
    at: +0s
    duration: 30s
    override:
      api-gateway.GET /search:
        error_rate: 50%
      api-gateway.POST /checkout:
        error_rate: 50%
      payment-service.charge:
        error_rate: 80%
```

### What if VIP traffic dominates?

Change the `customer.tier` attribute weights so most traffic is VIP:

```yaml
attributes:
  customer.tier:
    values:
      standard: 10
      vip: 90
```

This verifies that your probabilistic baseline still applies when the attribute-based policy matches most traces.

### Test with scenarios labelled

Use the `--label-scenarios` flag to add `synth.scenario` attributes to spans, so you can see which scenario was active when a trace was generated:

```sh
motel run --stdout --duration 15s --label-scenarios \
  docs/examples/tail-sampling-test.yaml | \
  jq -r 'select(.Parent.SpanID == "0000000000000000") |
    (.Attributes[] | select(.Key == "synth.scenarios") |
    .Value.Value | if length > 0 then join(",") else "none" end) // "none"' | \
  sort | uniq -c
```

## Further reading

- [DSL reference](../../cmd/motel/README.md) -- full topology schema including scenarios
- [Model your services](model-your-services.md) -- writing topologies from scratch or importing from traces
- [Tail sampling processor docs](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor/tailsamplingprocessor) -- full list of policy types and configuration options
