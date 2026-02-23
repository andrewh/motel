# Test Alert Thresholds

This guide shows how to use motel to verify that your alerting rules fire when they should. You will set up a baseline topology, inject a degradation with a scenario, and confirm the alert triggers within the expected window.

## Set up a baseline topology

Start with a topology that represents your service under normal conditions. The key parameters are a realistic error rate, typical latency, and enough traffic to produce a statistically meaningful signal.

```yaml
version: 1

services:
  web-gateway:
    operations:
      GET /api:
        duration: 45ms +/- 12ms
        error_rate: 0.1%
        calls:
          - backend.handle-request

  backend:
    operations:
      handle-request:
        duration: 30ms +/- 8ms
        error_rate: 0.05%

traffic:
  rate: 100/s
```

A rate of 100/s is a reasonable starting point. At lower rates (say 1-5/s), per-second error rate calculations become noisy and alerts may flicker. Higher rates produce smoother metrics but more data. Match the rate to what your real service handles, or at least keep it high enough that a 5-minute evaluation window sees thousands of spans.

Validate and run a short burst to check the output looks right:

```sh
motel validate alert-test.yaml
motel run --stdout --duration 5s alert-test.yaml | head -20
```

## Inject a degradation with a scenario

Scenarios overlay time-windowed overrides onto the baseline. To test an error rate alert, inject elevated errors at a known offset:

```yaml
scenarios:
  - name: error spike
    at: +2m
    duration: 8m
    override:
      web-gateway.GET /api:
        error_rate: 10%
```

This keeps the first two minutes at baseline (0.1% errors), then raises the error rate to 10% for eight minutes.

You can test latency alerts the same way:

```yaml
scenarios:
  - name: latency spike
    at: +2m
    duration: 8m
    override:
      backend.handle-request:
        duration: 500ms +/- 100ms
```

Add the scenario block to your topology file under the `scenarios:` key.

## Match scenario duration to alerting windows

An alert that evaluates over a 5-minute window needs the degraded condition to persist for at least 5 minutes. If your scenario is shorter than the evaluation window, the alert may never fire.

Consider the full timeline:

1. **Startup settling** -- the first few seconds of motel output may not represent steady state as spans complete at different times. Allow a buffer before the scenario begins.
2. **Scenario onset** -- the degradation starts at the `at` offset.
3. **Evaluation window fills** -- the alerting backend needs a full window of degraded data before it can trigger.
4. **Evaluation frequency** -- if the alert checks every 60 seconds with a 5-minute window, the condition must hold across multiple evaluation cycles.

A safe rule of thumb: set the scenario duration to at least 1.5 times the evaluation window. For a 5-minute window, use 8 minutes of degraded traffic.

## Account for sampling

Alerts fire on metrics, but those metrics can come from different points in the pipeline — before sampling, after sampling, or from application-level instrumentation. The point at which metrics are derived determines what error rate motel needs to produce.

### Metrics from sampled traces

If your pipeline derives RED metrics (rate, errors, duration) from traces after a tail sampler, the sampler distorts what the metrics see. A tail sampler that keeps all error traces will overrepresent errors in the derived metrics. A 1% true error rate might appear as 10% or higher, depending on the sampling policy. To test a specific threshold, you need to work backwards from the sampler's behaviour to determine what error rate motel should produce.

### Span metrics connector (pre-sampling)

If you use the OpenTelemetry Collector's span metrics connector before the sampling stage, metrics reflect the true rate. Motel's configured `error_rate` maps directly to what your alerts observe. This is the simplest case.

### Application-level metrics

Motel can generate metrics directly, bypassing trace sampling entirely:

```sh
motel run --signals metrics --duration 15m --endpoint http://localhost:4318 alert-test.yaml
```

This is the most predictable path for alert testing. The configured error rate is exactly what your alerting backend sees, with no sampling distortion.

**Recommendation:** understand where in your pipeline metrics are derived. If metrics come from sampled traces, calibrate motel's error rates to account for the sampler. If metrics come from a pre-sampling connector or directly from motel, use the target error rate directly.

## Worked example

Suppose you have an alert rule: "fire when the error rate for `web-gateway GET /api` exceeds 5% for 5 minutes."

### 1. Write the topology

```yaml
version: 1

services:
  web-gateway:
    operations:
      GET /api:
        duration: 45ms +/- 12ms
        error_rate: 0.1%
        calls:
          - backend.handle-request

  backend:
    operations:
      handle-request:
        duration: 30ms +/- 8ms
        error_rate: 0.05%

traffic:
  rate: 100/s

scenarios:
  - name: error spike
    at: +2m
    duration: 8m
    override:
      web-gateway.GET /api:
        error_rate: 10%
```

The baseline error rate (0.1%) is well below the 5% threshold. The scenario injects 10% errors -- comfortably above the threshold to avoid borderline cases.

### 2. Run motel

Send telemetry to your collector for the full duration of baseline plus scenario:

```sh
motel run --duration 12m --endpoint http://localhost:4318 alert-test.yaml
```

Use `--label-scenarios` if you want scenario names attached to spans for debugging:

```sh
motel run --duration 12m --label-scenarios --endpoint http://localhost:4318 alert-test.yaml
```

### 3. Predict when the alert fires

- **T+0m to T+2m**: baseline at 0.1% errors. No alert.
- **T+2m**: scenario begins, error rate rises to 10%.
- **T+2m to T+7m**: the 5-minute evaluation window fills with degraded data.
- **Around T+7m**: the alert should fire, depending on evaluation frequency and any pending-period configuration.

If your alert has a "for" / pending duration of 2 minutes, add that to the expected time: the alert fires around T+9m.

### 4. Verify

Check your alerting backend (Prometheus Alertmanager, Grafana, PagerDuty, or wherever alerts route) to confirm the alert fired within the expected window. If it did not:

- Check that metrics are arriving in your backend (`--signals traces,metrics` or confirm your span metrics connector is running).
- Verify the alert rule's label matchers correspond to the service and operation names motel produces.
- Review the sampling section above if derived error rates do not match expectations.

## Further reading

- [Model your services](model-your-services.md) -- creating and refining topologies
- [CLI reference](../reference/synth.md) -- all CLI flags and output formats
