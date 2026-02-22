# Test OTTL Transformations

This guide shows how to use motel to test OpenTelemetry Transformation Language (OTTL) rules with fast, repeatable feedback. You will design a topology that produces spans with attributes worth transforming, run those spans through a collector with OTTL processors, and verify the output.

## Prerequisites

- motel installed
- An OpenTelemetry Collector binary (the [contrib distribution](https://github.com/open-telemetry/opentelemetry-collector-contrib) includes the `transform` processor)

## 1. Design a topology with messy attributes

Real telemetry is messy. Attributes use inconsistent naming conventions, carry values that belong at a different level, or contain compound strings that should be split. Design a topology that reproduces these problems so you have concrete data to transform.

The example topology at [`docs/examples/ottl-transforms.yaml`](../examples/ottl-transforms.yaml) generates spans with several common issues:

- **Mixed naming conventions** — `httpStatusCode` (camelCase) alongside `http.request.method` (dotted)
- **Compound values** — `request.metadata` contains `"region=eu-west-1;priority=high"` that should be separate attributes
- **Wrong attribute level** — `datacenter` appears on spans but belongs on the service resource
- **Inconsistent conventions** — `notification_type` (underscores) vs `deployment.environment` (dots)
- **PII leakage** — `user.email` that should be redacted

## 2. Capture baseline output

Generate spans to stdout to see what the raw attributes look like before any transformation:

```sh
motel run --stdout --duration 5s docs/examples/ottl-transforms.yaml | head -20
```

Pick a few representative spans and note the attributes you want to change. For example, you might see:

```json
{
  "Name": "POST /api/checkout",
  "Attributes": {
    "httpStatusCode": "200",
    "request.metadata": "region=eu-west-1;priority=high",
    "customer.id": "cust-42"
  }
}
```

## 3. Write a collector config with OTTL rules

Create a collector configuration that receives OTLP, applies transforms, and exports to stdout so you can inspect the results. Save this as `collector-ottl.yaml`:

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  transform:
    trace_statements:
      - context: span
        statements:
          # Rename camelCase attribute to dotted convention
          - set(attributes["http.response.status_code"], Int(attributes["httpStatusCode"]))
            where attributes["httpStatusCode"] != nil
          - delete_key(attributes, "httpStatusCode")
            where attributes["http.response.status_code"] != nil

          # Rename underscore attributes to dotted convention
          - set(attributes["notification.type"], attributes["notification_type"])
            where attributes["notification_type"] != nil
          - delete_key(attributes, "notification_type")
            where attributes["notification.type"] != nil
          - set(attributes["notification.channel"], attributes["notification_channel"])
            where attributes["notification_channel"] != nil
          - delete_key(attributes, "notification_channel")
            where attributes["notification.channel"] != nil

          # Redact PII
          - replace_pattern(attributes["user.email"], "^.*$", "REDACTED")
            where attributes["user.email"] != nil

exporters:
  debug:
    verbosity: detailed

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [transform]
      exporters: [debug]
```

## 4. Run motel through the collector

Start the collector, then send motel's output to it:

```sh
# Terminal 1: start the collector
otelcol-contrib --config collector-ottl.yaml

# Terminal 2: send traces
motel run --endpoint localhost:4317 --protocol grpc \
  --duration 5s docs/examples/ottl-transforms.yaml
```

The collector's debug exporter prints transformed spans to its stderr. Check that:

- `httpStatusCode` is now `http.response.status_code`
- `notification_type` is now `notification.type`
- `user.email` shows `REDACTED`

## 5. Compare before and after

For a structured comparison, export both the raw and transformed output. Use the HTTP protocol with a file exporter in the collector config, or compare the motel stdout output against the collector debug output:

```sh
# Raw output (before transforms)
motel run --stdout --duration 5s docs/examples/ottl-transforms.yaml \
  | jq -r '.Attributes | keys[]' | sort -u > attrs-before.txt

# Send through collector, capture its debug output
otelcol-contrib --config collector-ottl.yaml 2> collector-output.txt &
COLLECTOR_PID=$!
sleep 2
motel run --endpoint localhost:4317 --protocol grpc \
  --duration 5s docs/examples/ottl-transforms.yaml
kill $COLLECTOR_PID

# Check that renamed attributes appear and originals are gone
grep "http.response.status_code" collector-output.txt
grep "httpStatusCode" collector-output.txt  # should find nothing
```

## 6. Iterate on your rules

The feedback loop is fast because motel generates deterministic, controllable traffic:

1. Edit the `transform` processor statements in your collector config
2. Restart the collector
3. Run motel again for a short burst (`--duration 2s` is usually enough)
4. Inspect the output

### Tips for iterating

- **Start with one rule at a time.** Add a single statement, verify it works, then add the next. OTTL errors are easier to diagnose in isolation.
- **Use `--duration 2s` and a low traffic rate** (the example uses `20/s`) so you get enough spans to verify without drowning in output.
- **Use weighted values to test conditional logic.** The example topology generates `httpStatusCode` with weighted values including `"500"` — you can write OTTL rules that behave differently for error status codes.
- **Test edge cases with attribute generators.** Use `sequence` to produce predictable IDs, `range` for numeric boundaries, and `values` with weights to control the distribution of attribute values your OTTL rules will encounter.

## Further reading

- [Model your services as a topology](model-your-services.md) — designing topologies from scratch
- [OTTL syntax reference](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/pkg/ottl) — upstream OTTL documentation
- [Transform processor](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor/transformprocessor) — collector processor docs
