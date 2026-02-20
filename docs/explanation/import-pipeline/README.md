# How `motel import` Builds a Topology from Traces

This document walks through the inference pipeline step by step, using
4 real traces generated from a small topology. Every decision the code
makes is shown against the actual data.

## Files in this directory

| File | Purpose |
|------|---------|
| `topology.yaml` | The source topology used to generate the traces |
| `traces.jsonl` | 10 curated spans (4 traces) in stdouttrace format |
| `inferred-topology.yaml` | The topology produced by `motel import` |

You can reproduce the import yourself:

```
motel import docs/explanation/worked-example/traces.jsonl
```

## The source topology

Three services, one root operation, one probabilistic call:

```
api.handle ──always──▶ database.query
     │
     └──50% chance──▶ cache.lookup
```

The `api` service has a `deployment.environment: staging` attribute and a
10% error rate on `handle`. See `topology.yaml` for the full definition.

## The raw trace data

We generated traces with `motel run --stdout` and hand-picked 4 that
illustrate different paths through the topology:

### Trace 1 (`017acb8b`): api → database + cache, no error

```
handle   [api]      span=7a48dd5f  parent=00000000  21:16:45.401 → .429  (28.3ms)  OK
├─ query [database] span=3073cfe2  parent=7a48dd5f  21:16:45.413 → .417  (4.4ms)   OK
└─ lookup[cache]    span=46509810  parent=7a48dd5f  21:16:45.413 → .414  (1.2ms)   OK
```

Both children start at the same time (.413) — this is a **parallel** call.

### Trace 2 (`009f737e`): api → database only, no error

```
handle   [api]      span=58edc0f5  parent=00000000  21:16:44.284 → .316  (32.1ms)  OK
└─ query [database] span=6424ff5b  parent=58edc0f5  21:16:44.299 → .302  (3.2ms)   OK
```

No cache call this time — the 50% probability meant this trace skipped it.

### Trace 3 (`09d79ff5`): api → database + cache, **error**

```
handle   [api]      span=da94c8aa  parent=00000000  21:16:44.183 → .211  (28.4ms)  ERROR
├─ query [database] span=e5acac49  parent=da94c8aa  21:16:44.192 → .202  (9.2ms)   OK
└─ lookup[cache]    span=e2cd9a6d  parent=da94c8aa  21:16:44.192 → .194  (1.3ms)   OK
```

The error is on `api.handle` itself — the children completed successfully.

### Trace 4 (`2c6c5e27`): api → database only, **error**

```
handle   [api]      span=28a23c72  parent=00000000  21:16:43.980 → .001  (20.6ms)  ERROR
└─ query [database] span=f3adceaf  parent=28a23c72  21:16:43.988 → .994  (6.1ms)   OK
```

## Stage 1: Parse spans

**Code**: `span.go` → `ParseSpans()`

The importer reads the 10 lines of JSON. Each line is a stdouttrace span
with `SpanContext`, `Parent`, `Name`, `StartTime`, `EndTime`, `Status`,
and `Attributes` fields.

The format is auto-detected: the first JSON object has a `SpanContext` key,
which identifies it as stdouttrace (as opposed to OTLP, which would have
`resourceSpans`).

Each span is normalised into a common `Span` struct:

- **TraceID** and **SpanID**: taken from `SpanContext`
- **ParentID**: taken from `Parent.SpanID` (`00000000...` means root)
- **Service**: extracted from the `synth.service` attribute
- **Operation**: the span `Name`
- **StartTime** / **EndTime**: parsed from RFC3339 timestamps
- **IsError**: true when `Status.Code == "Error"`
- **Attributes**: all non-internal attributes (excluding `synth.*` keys)

After parsing, we have 10 `Span` values — a flat list with no structure.

## Stage 2: Build trace trees

**Code**: `tree.go` → `BuildTrees()`

The flat spans are grouped by TraceID (4 groups), then linked into trees
by matching each span's ParentID to another span's SpanID.

For trace `017acb8b`:

1. Index all spans by SpanID: `{7a48dd5f: handle, 3073cfe2: query, 46509810: lookup}`
2. `handle` has parent `00000000` (all zeros) → it's a **root**
3. `query` has parent `7a48dd5f` → child of `handle`
4. `lookup` has parent `7a48dd5f` → child of `handle`

Result:

```
handle (root)
├── query
└── lookup
```

The same process produces 4 trees:

| Trace | Tree |
|-------|------|
| `017acb8b` | handle → [query, lookup] |
| `009f737e` | handle → [query] |
| `09d79ff5` | handle → [query, lookup] |
| `2c6c5e27` | handle → [query] |

## Stage 3: Collect statistics

**Code**: `stats.go` → `StatsCollector.CollectFromTrees()`

The collector walks each tree recursively, accumulating per-operation data.

### Duration statistics

For each (service, operation) pair, every span's duration is recorded:

| Service | Operation | Durations (ms) | Mean | StdDev |
|---------|-----------|-----------------|------|--------|
| api | handle | 28.3, 32.1, 28.4, 20.6 | 27.4ms | 4.8ms |
| database | query | 4.4, 3.2, 9.2, 6.1 | 5.7ms | 2.6ms |
| cache | lookup | 1.2, 1.3 | 1.3ms | 83µs |

The mean and sample standard deviation (n-1 denominator) are computed by
`MeanDuration()` and `StdDevDuration()`. When stddev is non-zero, the
duration is formatted as `mean +/- stddev` (e.g. `27ms +/- 4.8ms`).

### Error counts

| Service | Operation | Total | Errors | Rate |
|---------|-----------|-------|--------|------|
| api | handle | 4 | 2 | 50% |
| database | query | 4 | 0 | — |
| cache | lookup | 2 | 0 | — |

`FormatErrorRate()` only emits an `error_rate` field when errors > 0.

### Call counts (for probability)

For each parent operation, the collector records how many times each child
was called and how many times the parent was invoked total:

| Parent | Child | Times called | Parent invocations | Probability |
|--------|-------|--------------|--------------------|-------------|
| api.handle | database.query | 4 | 4 | 4/4 = 1.0 |
| api.handle | cache.lookup | 2 | 4 | 2/4 = 0.5 |

`database.query` appears in all 4 traces → probability 1.0 (always called).
`cache.lookup` appears in 2 of 4 traces → probability 0.5.

In the YAML output, probability 1.0 is omitted (the call is listed as a
plain string target). Probability < 1.0 is written as a mapping with an
explicit `probability` field.

## Stage 4: Infer call style

**Code**: `stats.go` → `isParallel()` / `isSequential()`

When a parent has 2+ children, the collector votes on whether the calls
were parallel or sequential by examining timestamps.

For traces 1 and 3 (the ones where `handle` calls both `query` and
`lookup`):

- **Trace 1**: `query` starts at .413, `lookup` starts at .413 — difference
  is 0ms, well within the 1ms threshold → **parallel**
- **Trace 3**: `query` starts at .192, `lookup` starts at .192 — same
  start time → **parallel**

Both votes are parallel, so no `call_style` field appears in the output
(parallel is the default). If the votes had favoured sequential, the YAML
would include `call_style: sequential`.

Traces 2 and 4 have only one child each, so no vote is cast.

## Stage 5: Detect service attributes

**Code**: `infer.go` → `inferServiceAttributes()`

The importer scans all spans and finds attributes that have the **same
value on every span** of a given service. These are promoted to
service-level attributes in the topology.

| Service | Attribute | Values seen | Constant? |
|---------|-----------|-------------|-----------|
| api | `deployment.environment` | `staging` (×4) | Yes — promoted |
| database | *(none)* | — | — |
| cache | *(none)* | — | — |

Internal attributes (`synth.service`, `synth.trace_id`, etc.) are excluded
from this analysis.

Result: only `api` gets an `attributes` section in the YAML.

## Stage 6: Compute traffic rate

**Code**: `infer.go` → `computeWindow()`

The traffic rate is calculated from root span timestamps:

- Earliest root: trace 4 at `21:16:43.980`
- Latest root: trace 1 at `21:16:45.401`
- Window: `1.42 seconds`
- Rate: `4 traces / 1.42s ≈ 3/s`

The rate is formatted as `3/s` in the traffic section.

## Stage 7: Marshal to YAML

**Code**: `marshal.go` → `MarshalConfig()`

All the collected data is assembled into the topology YAML format. Services
and operations are sorted alphabetically for deterministic output.

Each decision maps to a line in `inferred-topology.yaml`:

```yaml
# Inferred from 4 traces (10 spans) observed over 1 seconds   ← trace/span count, window
version: 1
services:
  api:
    attributes:
      deployment.environment: staging       ← stage 5: constant attribute
    operations:
      handle:
        duration: 27ms +/- 4.8ms            ← stage 3: mean +/- stddev
        error_rate: 50%                      ← stage 3: 2 errors / 4 total
        calls:
          - target: cache.lookup             ← stage 3: probability < 1.0
            probability: 0.5                 ←   so written as mapping
          - database.query                   ← stage 3: probability = 1.0
  cache:                                     ←   so written as plain string
    operations:
      lookup:
        duration: 1.3ms +/- 83µs
  database:
    operations:
      query:
        duration: 5.7ms +/- 2.6ms
traffic:
  rate: 3/s                                  ← stage 6: 4 traces / 1.42s
```

Note: no `call_style` field appears because all votes were parallel
(the default).

## Stage 8: Round-trip validation

**Code**: `infer.go` → `validateRoundTrip()`

As a final safety check, the generated YAML is written to a temp file and
loaded back through `synth.LoadConfig()` and `synth.ValidateConfig()`. This
catches any inconsistency between the marshal format and what the synth
engine expects — duration formats that don't parse, unknown fields,
broken call references, etc.

If round-trip validation fails, the import returns an error rather than
emitting a topology that `motel run` would reject.

```
$ motel validate docs/explanation/worked-example/inferred-topology.yaml
Configuration valid: 3 services, 1 root operation
```

## What wasn't inferred

The import produces a starting point, not a finished topology. Things
the importer cannot determine from trace data alone:

- **Scenario overrides** — there's no way to know which behaviour changes
  are intentional vs normal operation
- **Traffic patterns** — only the average rate is computed, not whether
  it's uniform, diurnal, poisson, or bursty
- **Queue depth, circuit breakers, backpressure** — these are simulation
  parameters, not observable from traces
- **Attribute distributions** — only constant attributes are detected.
  Per-span varying attributes (like request IDs) are dropped
- **Duration distribution shape** — the synth engine uses a normal
  distribution, but real durations are often log-normal or bimodal

The header comment in the output reminds users to review and adjust.
