# Validating motel check Against Published Trace Studies

`motel check` computes static worst-case bounds and Monte Carlo percentile
distributions for trace depth, fan-out, and span count. The Alibaba and Meta
trace studies report empirical distributions for the same structural metrics,
measured from production traffic. This document validates `check` against
those independently published numbers: topologies under `docs/examples/` were
constructed to exhibit the reported characteristics, and the test suite in
`pkg/synth/empirical_test.go` verifies that `check` reproduces them.

## Method

1. Model the structural characteristics each paper reports as a topology
   (`docs/examples/alibaba-call-graph.yaml`, `docs/examples/meta-wide-fanout.yaml`).
2. Verify the static analysis (`MaxDepth`, `MaxFanOut`, `MaxSpans`) returns
   exactly the values the topology was constructed to exhibit.
3. Run Monte Carlo sampling (1000 traces, fixed seed) and verify the
   p50/p95/p99/max distributions stay within the published ranges and never
   exceed the static bounds.

This validates the analysis engine in both directions: the static DFS agrees
with hand-computed expectations on realistic graph shapes, and the simulation
engine produces trace populations consistent with both the static bounds and
the published distributions.

## Alibaba (Luo et al., IEEE TPDS 2022)

The paper analyses production call graphs from Alibaba clusters and reports:

| Published metric           | Reported value          | Modelled in topology         | check agrees |
| -------------------------- | ----------------------- | ---------------------------- | ------------ |
| Call graph depth           | 2–6 for top services    | Longest path depth 6         | Yes          |
| Children set sizes         | 1–10                    | Max fan-out 10               | Yes          |
| Repeated call rate         | 16.2%                   | 3 of 18 call edges (16.7%)   | Yes          |
| Cache-dominated leaf tier  | Heavy memcached access  | memcached is the common leaf | n/a (shape)  |

`motel check --seed 42 docs/examples/alibaba-call-graph.yaml` reports:

```
PASS  max-depth: 6 (limit: 10)
      path: gateway.POST /checkout → orchestrator.compose → product.detail → inventory.check → reservation.hold → cache.get → memcached.get
      p50: 6  p95: 6  p99: 6  max: 6  (1000 samples)
PASS  max-fan-out: 10 (limit: 100)
      worst: orchestrator.compose
      p50: 8  p95: 10  p99: 10  max: 10  (1000 samples)
PASS  max-spans: 24 static worst-case, 24 observed/1000 samples (limit: 10000)
      p50: 19  p95: 24  p99: 24  max: 24  (1000 samples)
```

The static bounds (depth 6, fan-out 10, spans 24) match hand-computed
expectations exactly. The sampled distributions behave as the model predicts:
fan-out varies between the deterministic floor (5 children) and the bound
(10) according to the per-call probabilities, span counts spread below the
worst case, and no sampled value exceeds a static bound.

The repeated call rate is checked against the topology definition rather
than `check` output: `check` does not report repeated calls as a metric, so
the test computes the fraction of call edges with `count > 1` directly from
the parsed config (16.7%, within tolerance of the published 16.2%).

## Meta (Huye et al., USENIX ATC 2023; Du et al., ICS 2025)

Huye et al. characterise Meta's request workflows as wide and shallow at the
aggregation tier; Du et al. quantify children set sizes of up to 50 at Meta
(versus 1–10 at Alibaba). The modelled topology is a feed-style workflow with
an aggregator fanning out to 40 ranking leaves plus cache and metadata calls.
For the public ATC 2023 summary data import workflow, see
[meta-trace-import.md](meta-trace-import.md).

| Published metric   | Reported value           | Modelled in topology | check agrees |
| ------------------ | ------------------------ | -------------------- | ------------ |
| Children set sizes | Up to 50 (Meta)          | Fan-out 50           | Yes          |
| Workflow shape     | Wide, shallow aggregation | Depth 3, 54 spans    | Yes          |

`motel check --seed 42 docs/examples/meta-wide-fanout.yaml` reports:

```
PASS  max-depth: 3 (limit: 10)
      path: web.GET /feed → feed-agg.rank → social-graph.follows → social-db.query
      p50: 3  p95: 3  p99: 3  max: 3  (1000 samples)
PASS  max-fan-out: 50 (limit: 100)
      worst: feed-agg.rank
      p50: 50  p95: 50  p99: 50  max: 50  (1000 samples)
PASS  max-spans: 54 static worst-case, 54 observed/1000 samples (limit: 10000)
      p50: 54  p95: 54  p99: 54  max: 54  (1000 samples)
```

This topology is fully deterministic, so it doubles as an exactness check:
every sampled trace must realise the static worst case, and the percentile
distributions must be constant. They are.

## Discrepancies and limitations

No disagreements between `check` and the modelled values were found. The
following published metrics could not be validated through `check` and are
documented as limitations:

- **Overlap rate (77.1%, Luo et al.).** Measures how often the same call
  graph topology recurs across traces of the same entry service. This is a
  cross-trace population metric; `check` analyses one topology and reports
  per-trace structure, so there is no corresponding output to compare. The
  simulation engine does produce overlapping topologies (probabilistic calls
  make trace shapes recur), but quantifying that would require a new metric.
- **Repeated call rate (16.2%, Luo et al.).** Validated against the topology
  definition, not `check` output, because `check` does not report repeated
  calls as a separate metric. Repeated calls are reflected in fan-out and
  span counts (a `count: 3` edge contributes 3 to its caller's fan-out).
- **Topology dynamics over time (Huye et al.).** Meta's paper reports churn
  in the service topology itself. motel models this with scenarios
  (`add_calls`/`remove_calls`), but `check` analyses the base topology
  without scenario overlays, so dynamics are out of scope here.
- **Depth distribution shape.** Luo et al. report that most call graphs are
  shallow with a long tail. A single topology cannot reproduce a
  population-level depth distribution across many distinct entry services;
  the modelled topology instead represents one deep (depth-6) entry service
  from the top of the published range. The property-based generator in
  `pkg/synth/check_test.go` (`genRealisticConfig`) covers the population
  view by drawing many topologies from the published distributions.

## Reproducing

```sh
make build
build/motel check --seed 42 docs/examples/alibaba-call-graph.yaml
build/motel check --seed 42 docs/examples/meta-wide-fanout.yaml
go test ./pkg/synth/ -run TestEmpirical -v
```

## References

- Luo et al., "An In-Depth Study of Microservice Call Graph and Runtime
  Performance," _IEEE TPDS_, 2022.
  [IEEE Xplore](https://ieeexplore.ieee.org/document/9774016/)
- Luo et al., "Characterizing Microservice Dependency and Performance:
  Alibaba Trace Analysis," _SoCC_, 2021.
  [ACM DL](https://dl.acm.org/doi/10.1145/3472883.3487003)
- Huye, Shkuro, and Sambasivan, "Lifting the Veil on Meta's Microservice
  Architecture," _USENIX ATC_, 2023.
  [USENIX](https://www.usenix.org/conference/atc23/presentation/huye)
- Du et al., "A Microservice Graph Generator with Production
  Characteristics," _ICS_, 2025.
  [arXiv](https://arxiv.org/html/2412.19083v1)
- [Related work survey](related-work.md)
