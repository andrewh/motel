# Saved previews

These files show what `motel preview` produces. Open an HTML file directly in a
browser; its topology, traffic chart, captured signals, and raw JSON are embedded.
The captured run is bounded, so it is an example of one simulation rather than
an exhaustive view of every scenario.

| Report | Source | Shows |
|--------|--------|-------|
| [Circuit breaker](preview-circuit-breaker.html) | [`circuit-breaker.yaml`](../circuit-breaker.yaml) | Service calls and scenario changes; 200 generated traces. |
| [Metrics](preview-metrics.html) | [`topology-driven-metrics.yaml`](../topology-driven-metrics.yaml) | Gauge, histogram, and sum data points. |
| [Logs](preview-logs.html) | [`topology-driven-logs.yaml`](../topology-driven-logs.yaml) | Log records with multiple severities. |
| [Resource attributes](preview-resource-attributes.html) | [`resource-attributes.yaml`](../resource-attributes.yaml) | Per-service resource fields in captured spans. |
| [Imported traces](imported-example-preview.html) | [`imported-example.yaml`](imported-example.yaml) | A topology inferred from the synthetic [`import-capture.jsonl`](../import-capture.jsonl) fixture; five regenerated traces. |

The imported topology's [evidence report](imported-example-evidence.md) records
what the importer inferred from the 100 source traces. The HTML report contains
newly generated traces from that topology; it does not replay the source spans.

From the repository root, rebuild these examples with:

```sh
make build
build/motel preview docs/examples/async-calls.yaml -o docs/examples/async-calls.svg
build/motel preview --format html --run-duration 8s docs/examples/circuit-breaker.yaml -o docs/examples/previews/preview-circuit-breaker.html
build/motel preview --format html --run-duration 500ms --max-traces 10 docs/examples/topology-driven-metrics.yaml -o docs/examples/previews/preview-metrics.html
build/motel preview --format html --run-duration 500ms --max-traces 10 docs/examples/topology-driven-logs.yaml -o docs/examples/previews/preview-logs.html
build/motel preview --format html --run-duration 500ms --max-traces 10 docs/examples/resource-attributes.yaml -o docs/examples/previews/preview-resource-attributes.html
build/motel import --report docs/examples/previews/imported-example-evidence.md docs/examples/import-capture.jsonl > docs/examples/previews/imported-example.yaml
build/motel preview --format html --duration 10s --run-duration 5s --seed 253 --max-traces 20 docs/examples/previews/imported-example.yaml -o docs/examples/previews/imported-example-preview.html
```

Simulation timestamps and trace IDs can change between runs.
