# Follow-along stack: collector + tail sampling + Jaeger

A ready-made Docker Compose stack for testing a collector pipeline with
motel: an OpenTelemetry Collector applying the tail sampling policies from
[Test tail sampling policies](../../how-to/test-tail-sampling.md), exporting
whatever survives to Jaeger.

```
motel ──OTLP──▶ collector (tail sampling) ──OTLP──▶ Jaeger UI
```

Jaeger's own OTLP ports are deliberately not exposed — all telemetry enters
through the collector, so what you see in the UI is what your sampling
policies kept.

## Start the stack

```sh
cd docs/examples/compose
docker compose up -d
```

This exposes:

- `4317` — collector OTLP gRPC receiver
- `4318` — collector OTLP HTTP receiver
- `16686` — Jaeger UI

## Send traffic

The [tail sampling test topology](../tail-sampling-test.yaml) generates a
predictable mix of normal, slow, error, and VIP traces, including a slow
database window at +3s and a payment outage at +8s:

```sh
motel run --endpoint localhost:4317 --protocol grpc \
  --duration 15s ../tail-sampling-test.yaml
```

Wait ~10 seconds after the run finishes for the sampling decision window
(`decision_wait: 5s`) to flush.

## Inspect results

Open <http://localhost:16686> and search for the `api-gateway` service.

The topology emits ~750 traces (50/s for 15s), but the policies keep any
trace that has an error, exceeds 500ms, or carries `customer.tier: vip` —
plus 5% of everything else. In one sample run, motel reported 729 traces
sent (57 containing errors) and Jaeger received 370: all 57 error traces,
99 VIP traces, and 278 traces slower than 500ms (categories overlap).
Expect the same shape, heavily skewed towards the interesting traces:

- **Errors** — filter with `error=true`; payment failures cluster in the
  +8s scenario window
- **Slow traces** — set min duration to `500ms`; database queries at
  ~800ms cluster in the +3s window
- **VIP traces** — search tag `customer.tier=vip`; roughly 10-15% of
  original traffic
- **Everything else** — fast, successful, non-VIP traces appear at ~5% of
  their original rate

To verify the ratios quantitatively, compare against motel's raw output —
see [step 4 of the tail sampling guide](../../how-to/test-tail-sampling.md#4-verify-what-gets-sampled).

## Experiment

- **See the unsampled firehose** — remove `tail_sampling` from the
  pipeline's `processors` list in [`otel-collector.yaml`](otel-collector.yaml)
  and `docker compose restart otel-collector`. Every trace now reaches
  Jaeger.
- **Change the policies** — edit the thresholds or add policies from the
  [tail sampling processor docs](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor/tailsamplingprocessor),
  restart the collector, and re-run motel.
- **Bring your own topology** — any topology works; the policies only get
  interesting when the traffic has errors, latency variation, or the
  `customer.tier` attribute.

## Clean up

```sh
docker compose down
```
