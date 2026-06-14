# Importing the Meta ATC 2023 Trace Summary Data

Issue 93 asks whether `motel import` can ingest the public Meta distributed
trace dataset released with Huye, Shkuro, and Sambasivan's USENIX ATC 2023
paper. The public repository is useful for this, but it does not publish raw
span exports. It publishes summary CSVs and a notebook that reproduces the
paper's figures.

This note records two import paths: a native `meta-summary` mode that streams
`parent-data.csv.gz` directly into a topology, and the older
`tools/meta2stdouttrace` converter that materializes a representative subset as
`stdouttrace` JSON for compatibility testing.

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

## Native Import

`motel import --format meta-summary` reads `parent-data.csv` or
`parent-data.csv.gz` directly. Each selected parent invocation contributes one
parent operation sample and one child operation sample per entry in
`children_set`:

- the parent and child ingress ids become `meta-*` services with an `invoke`
  operation
- `num_calls` weights downstream call probability so high-volume
  `(parent_name, children_set)` rows contribute more than rare rows
- `num_returning_calls` estimates the parent operation `error_rate` from calls
  that did not return
- `concurrency_rate > 0` votes for parallel call style; otherwise multi-child
  rows vote for sequential call style, again weighted by `num_calls`
- `--profile ads|fetch|raas` filters rows by the released profile column
- empty `children_set` rows are skipped unless `--include-empty` is set

This path does not reconstruct complete multi-hop traces because the public
parent rows do not include trace identifiers linking parent invocations back
into workflows.

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

This remains useful as an ingestion harness for the generic stdouttrace parser.
It proves that `motel import` can ingest a converted subset of the released
public data and infer a valid topology from the parent-child observations.

## Reproduction

Download the parent invocation data:

```sh
mkdir -p /tmp/motel-meta
curl -L -o /tmp/motel-meta/parent-data.csv.gz \
  https://raw.githubusercontent.com/facebookresearch/distributed_traces/main/summary_data_atc23/data/parent-data.csv.gz
```

Build motel and import the Ads profile directly from the compressed CSV:

```sh
make build
build/motel import --format meta-summary --profile ads --min-traces 100 \
  /tmp/motel-meta/parent-data.csv.gz \
  > /tmp/motel-meta/ads-parent-summary.yaml
```

For a smaller compatibility sample, generate deterministic `stdouttrace` JSON:

```sh
go run ./tools/meta2stdouttrace \
  -input /tmp/motel-meta/parent-data.csv.gz \
  -profile ads \
  -limit 1000 \
  > /tmp/motel-meta/ads-parent-sample.jsonl
```

Then import the sample:

```sh
build/motel import --format stdouttrace --min-traces 100 \
  /tmp/motel-meta/ads-parent-sample.jsonl \
  > /tmp/motel-meta/ads-parent-sample.yaml
```

Confidence warnings are expected with a 1,000-row subset because many inferred
operations and call probabilities have fewer than 100 samples.

Validate and analyze the subset topology:

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

## Full-Size Local Workflow

The native `meta-summary` path is intended for the full
`parent-data.csv.gz`, not just the 1,000-row compatibility sample. The
compressed file is small enough to download quickly, but it expands to about
1.16 GB and contains 40.2 million rows, so keep the dataset and generated YAML
under a scratch directory and redirect warnings and analysis output to files.

Download the summary files used by the import and comparison steps:

```sh
mkdir -p /tmp/motel-meta
base=https://raw.githubusercontent.com/facebookresearch/distributed_traces/main/summary_data_atc23/data
for file in \
  parent-data.csv.gz \
  trace-data.csv.gz \
  service-characteristics.csv \
  service-endpoints.csv \
  service-history.csv \
  inferred-path-data.csv
do
  curl -L -o "/tmp/motel-meta/$file" "$base/$file"
done
```

Build the current motel binary:

```sh
make build
```

Import each published profile from the complete parent invocation CSV:

```sh
for profile in ads fetch raas
do
  build/motel import --format meta-summary --profile "$profile" \
    /tmp/motel-meta/parent-data.csv.gz \
    > "/tmp/motel-meta/${profile}-parent-summary.yaml" \
    2> "/tmp/motel-meta/${profile}-parent-summary.warnings.txt"
done
```

To import all profiles into one topology, omit `--profile`:

```sh
build/motel import --format meta-summary \
  /tmp/motel-meta/parent-data.csv.gz \
  > /tmp/motel-meta/all-parent-summary.yaml \
  2> /tmp/motel-meta/all-parent-summary.warnings.txt
```

By default, empty `children_set` rows are skipped so the generated topology
focuses on observed parent-to-child calls. Add `--include-empty` when you also
want parent ingress ids that only appear with no children.

Validate and check every generated topology:

```sh
for topology in /tmp/motel-meta/*-parent-summary.yaml
do
  name=$(basename "$topology" .yaml)
  build/motel validate "$topology" \
    > "/tmp/motel-meta/${name}.validate.txt"
  build/motel check --seed 42 --samples 1000 \
    --max-depth 20 --max-fan-out 200000 --max-spans 200000 \
    "$topology" \
    > "/tmp/motel-meta/${name}.check.txt"
done
```

The relaxed check limits make this an analysis run rather than an assertion
that the topology is small. Use `--samples 0` for a faster static-only pass, or
`--sample-strategy swarm` when you want to exercise rare call branches more
aggressively than random sampling.

Summarize the released trace-level and service-level CSVs alongside motel's
generated topology checks:

```sh
python3 - <<'PY'
import csv
import gzip
from collections import defaultdict
from pathlib import Path

root = Path("/tmp/motel-meta")

def int_value(row, key):
    value = row.get(key, "")
    return int(float(value)) if value else 0

def child_count(value):
    value = value.strip()
    if not value or value == "set()":
        return 0
    return value.count(",") + 1

trace_stats = defaultdict(lambda: {
    "rows": 0,
    "max_depth": 0,
    "max_width": 0,
    "max_trace_size": 0,
    "max_services": 0,
})
with gzip.open(root / "trace-data.csv.gz", "rt", newline="") as f:
    for row in csv.DictReader(f):
        stats = trace_stats[row["profile"]]
        stats["rows"] += 1
        stats["max_depth"] = max(stats["max_depth"], int_value(row, "call_depth"))
        stats["max_width"] = max(stats["max_width"], int_value(row, "max_width"))
        stats["max_trace_size"] = max(
            stats["max_trace_size"], int_value(row, "trace_size"))
        stats["max_services"] = max(
            stats["max_services"], int_value(row, "num_services"))

parent_stats = defaultdict(lambda: {
    "rows": 0,
    "nonempty_rows": 0,
    "max_children_set": 0,
})
with gzip.open(root / "parent-data.csv.gz", "rt", newline="") as f:
    for row in csv.DictReader(f):
        count = child_count(row["children_set"])
        stats = parent_stats[row["profile"]]
        stats["rows"] += 1
        if count:
            stats["nonempty_rows"] += 1
        stats["max_children_set"] = max(stats["max_children_set"], count)

service_rows = 0
service_stats = {"max_fan_out_all": 0, "max_fan_in_all": 0, "max_instances": 0}
with open(root / "service-characteristics.csv", newline="") as f:
    for row in csv.DictReader(f):
        service_rows += 1
        service_stats["max_fan_out_all"] = max(
            service_stats["max_fan_out_all"], int_value(row, "num_fan_out_all"))
        service_stats["max_fan_in_all"] = max(
            service_stats["max_fan_in_all"], int_value(row, "num_fan_in_all"))
        service_stats["max_instances"] = max(
            service_stats["max_instances"], int_value(row, "num_instances"))

print("trace-data.csv.gz")
for profile, stats in sorted(trace_stats.items()):
    print(profile, stats)

print("\nparent-data.csv.gz")
for profile, stats in sorted(parent_stats.items()):
    print(profile, stats)

print("\nservice-characteristics.csv")
print({"rows": service_rows, **service_stats})
PY
```

Use the `trace-data.csv.gz` numbers to compare published full-workflow depth,
width, trace size, and services per trace against the one-hop topology inferred
from `parent-data.csv.gz`. Use the `parent-data.csv.gz` summary and
`motel check` output to inspect local parent fan-out and generated trace size.
The generated topology is expected to preserve `num_calls`-weighted
parent-child probabilities from the summary data, but it cannot recover the
original multi-hop workflows.

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

- Extend bounded-memory import beyond the Meta summary path. Explicit
  `stdouttrace` input is scanned incrementally, but OTLP and Jaeger JSON still
  require whole-document parsing and retain the 256 MB safety cap.
- Investigate whether Meta can publish raw trace exports or trace identifiers
  that link `parent-data.csv.gz` rows back into complete workflows. That would
  allow `motel import` to validate the paper's depth and span-count
  distributions directly rather than through one-hop parent samples.
