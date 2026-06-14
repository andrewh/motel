# Importing the Meta ATC 2023 Trace Summary Data

Issue 93 asks whether `motel import` can ingest the public Meta distributed
trace dataset released with Huye, Shkuro, and Sambasivan's USENIX ATC 2023
paper. The public repository is useful for this, but it does not publish raw
span exports. It publishes summary CSVs and a notebook that reproduces the
paper's figures.

This note records the current import path: convert a representative subset of
`parent-data.csv.gz` into motel's `stdouttrace` JSON, import it, validate the
resulting topology, and compare the imported topology against the metrics in
the released summary data.

## Source Data

Repository: <https://github.com/facebookresearch/distributed_traces>

License: CC BY-NC 4.0. Do not vendor the dataset into this repository. Download
it during reproduction and preserve attribution if sharing derived artifacts.

The upstream `summary_data_atc23/data/` directory currently contains:

| File | Compressed size | Uncompressed size or row count | Role |
| ---- | --------------- | ------------------------------ | ---- |
| `trace-data.csv.gz` | 15 MB | 104 MB, 6,589,078 rows | One row per trace with `trace_size`, `num_services`, `call_depth`, `max_width`, and `profile` |
| `parent-data.csv.gz` | 18 MB | 1.16 GB, 40,200,091 rows | One row per parent invocation with `parent_name`, `children_set`, call counts, concurrency, and `profile` |
| `service-characteristics.csv` | 568 KB | 18,571 rows | One row per service on 2022-12-21 with fan-in, fan-out, and instance counts |
| `service-endpoints.csv` | 231 KB | 13,030 rows | Thrift endpoint counts for services that communicate using Thrift RPC |
| `service-history.csv` | 32 KB | 659 rows | Service and instance counts over time |
| `inferred-path-data.csv` | 1.4 KB | 37 rows | Percent of paths ending at inferred services by depth and profile |

The upstream notebook defines `parent-data.csv.gz` as one row per invocation of
a parent ingress id. The row has a `children_set`, but no original trace id,
span id, parent span id, timestamp, or duration. Because of that, the public
data cannot be imported as raw traces directly.

## Converter

`tools/meta2stdouttrace` turns `parent-data.csv.gz` into `stdouttrace` JSON.
Each selected parent invocation becomes one synthetic trace:

- the parent ingress id becomes the root span
- each entry in `children_set` becomes a child span
- `concurrency_rate > 0` starts child spans in parallel; otherwise children are
  sequenced
- `meta.ingress_id`, `meta.profile`, `meta.num_calls`,
  `meta.num_returning_calls`, and `meta.concurrency_rate` are preserved as span
  attributes

This is an ingestion harness, not a reconstruction of the original Meta traces.
It proves that `motel import` can ingest a converted subset of the released
public data and infer a valid topology from the parent-child observations. It
does not reproduce the paper's full workflow depth because the released
parent-data rows are not linked back into complete traces.

## Reproduction

Download the parent invocation data:

```sh
mkdir -p /tmp/motel-meta
curl -L -o /tmp/motel-meta/parent-data.csv.gz \
  https://raw.githubusercontent.com/facebookresearch/distributed_traces/main/summary_data_atc23/data/parent-data.csv.gz
```

Generate a deterministic 1,000-invocation Ads-profile sample:

```sh
go run ./tools/meta2stdouttrace \
  -input /tmp/motel-meta/parent-data.csv.gz \
  -profile ads \
  -limit 1000 \
  > /tmp/motel-meta/ads-parent-sample.jsonl
```

Build motel and import the sample:

```sh
make build
build/motel import --format stdouttrace --min-traces 100 \
  /tmp/motel-meta/ads-parent-sample.jsonl \
  > /tmp/motel-meta/ads-parent-sample.yaml
```

Confidence warnings are expected with a 1,000-row subset because many inferred
operations and call probabilities have fewer than 100 samples.

Validate and analyze the imported topology:

```sh
build/motel validate /tmp/motel-meta/ads-parent-sample.yaml
build/motel check --seed 42 /tmp/motel-meta/ads-parent-sample.yaml
```

Verified locally on 2026-06-14:

```text
Configuration valid: 130 services, 55 root operations

PASS  max-depth: 1 (limit: 10)
      path: meta-00986.invoke -> meta-aajb.invoke
      p50: 1  p95: 1  p99: 1  max: 1  (1000 samples)
PASS  max-fan-out: 22 (limit: 100)
      worst: meta-00986-00076.invoke
      p50: 2  p95: 7  p99: 22  max: 22  (1000 samples)
PASS  max-spans: 23 static worst-case, 23 observed/1000 samples (limit: 10000)
      p50: 3  p95: 8  p99: 23  max: 23  (1000 samples)
```

## Comparison

The public summary data reports substantially larger production-scale
structure than the 1,000-invocation import sample:

| Metric | Released summary data | Imported subset |
| ------ | --------------------- | --------------- |
| Service-like entities | 18,571 services in `service-characteristics.csv` | 130 ingress-id services |
| Trace or invocation rows | 6,589,078 trace rows; 40,200,091 parent invocation rows | 1,000 parent invocation traces |
| Maximum workflow depth | 19 in `trace-data.csv.gz` | 1 |
| Maximum width or fan-out | 166,171 max trace width; 5,865 service fan-out | 22 max child spans |
| Maximum spans per trace | 1,702,486 `trace_size` | 23 |

The depth mismatch is expected. `parent-data.csv.gz` gives isolated
parent-to-children observations, so the converter can validate import of wide
local fan-out but cannot reconstruct multi-hop workflows. The trace-level CSV
does contain the published depth, width, and trace-size distributions, but not
the span records needed by `motel import`.

## Follow-Up Work

- Add a native summary-data path that produces a topology directly from
  `parent-data.csv.gz`, preserving parent call probabilities without first
  materializing synthetic span JSON.
- Add an importer mode or separate tool for large streaming inputs. The current
  trace importer reads the whole input and caps it at 256 MB, while a full
  synthetic expansion of 40.2 million parent invocations would be much larger.
- Investigate whether Meta can publish raw trace exports or trace identifiers
  that link `parent-data.csv.gz` rows back into complete workflows. That would
  allow `motel import` to validate the paper's depth and span-count
  distributions directly rather than through one-hop parent samples.
