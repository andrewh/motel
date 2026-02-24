# Related Work: Structural Analysis of Service Topologies

`motel check` computes static worst-case bounds and percentile distributions
for trace depth, fan-out, and span count from a topology definition. This
document surveys related academic and industry work.

## What motel check does

Given a YAML topology (services, operations, calls with count/retries/
probability/conditions), `check`:

1. Computes **static worst-case** depth, fan-out, and span count via DFS on
   the call graph, accounting for multiplicative effects of count and retries.
2. Runs **Monte Carlo simulation** (N sampled traces through an in-memory
   engine) and reports p50/p95/p99/max for each metric.
3. Compares both against configurable limits, reporting pass/fail per check.

## Empirical trace characterisation

The closest empirical work measures the same structural properties we compute,
but from production traces rather than a model.

- Luo et al., "An In-Depth Study of Microservice Call Graph and Runtime
  Performance," _IEEE Transactions on Parallel and Distributed Systems_, 2022.
  Alibaba study reporting call graph depth (2--6 for top services), width (up
  to 14+), children set sizes (1--10), repeated call rates (16.2%), and overlap
  rates (77.1%). Measures exactly the metrics `check` reports.
  [IEEE Xplore](https://ieeexplore.ieee.org/document/9774016/)

- Huye, Shkuro, and Sambasivan, "Lifting the Veil on Meta's Microservice
  Architecture: Analyses of Topology and Request Workflows," _USENIX ATC_, 2023. Characterises Meta's 18,500+ services including topology dynamics and
  request workflow structure. Released public trace data.
  [USENIX](https://www.usenix.org/conference/atc23/presentation/huye) |
  [PDF](https://www.usenix.org/system/files/atc23-huye.pdf)

- Luo et al., "Characterizing Microservice Dependency and Performance: Alibaba
  Trace Analysis," _ACM Symposium on Cloud Computing (SoCC)_, 2021. Earlier
  companion to the IEEE TPDS paper above, focusing on dependency
  characterisation.
  [ACM DL](https://dl.acm.org/doi/10.1145/3472883.3487003)

- "Complexity at Scale: A Quantitative Analysis of an Alibaba Microservice
  Deployment," _arXiv_, 2025. Reports long-tailed distributions of
  characteristics across tens of thousands of microservices.
  [arXiv](https://arxiv.org/html/2504.13141v1)

These papers do the inverse of what `check` does -- they observe production
traffic to measure trace shape. `check` predicts it from a model. The metrics
are the same.

## Static worst-case analysis

The static DFS in `check` is analogous to worst-case execution time (WCET)
analysis, but for trace structure rather than timing.

- Wilhelm et al., "The Worst-Case Execution Time Problem -- Overview of
  Methods and Survey of Tools," _ACM Transactions on Embedded Computing
  Systems_, 2008. WCET analysis traverses control-flow graphs to compute timing
  bounds; `check` traverses call graphs to compute structural bounds (depth,
  span count, fan-out). Same graph-traversal technique, different domain.
  [PDF](https://www.cs.fsu.edu/~whalley/papers/tecs07.pdf)

No paper was found that formalises worst-case structural bound analysis on
distributed call graphs specifically. The technique appears to be a novel
application of WCET-style reasoning to trace shape.

## Model-based performance prediction

`check` follows the philosophy of analysing quantitative models of a system
rather than observing the system itself, an approach established in two
research traditions.

**Software Performance Engineering (SPE):**

- Smith, _Performance Engineering of Software Systems_, Addison-Wesley, 1990.
- Smith and Williams, _Performance Solutions: A Practical Guide to Creating
  Responsive, Scalable Software_, Addison-Wesley, 2002.

SPE advocates predicting behaviour from architectural models rather than
waiting for measurements. It focuses on response time using execution graphs
and queueing models, not structural trace properties, but the model-first
analysis philosophy is the same.

**Layered Queueing Networks (LQNs):**

- Franks et al., "Enhanced Modeling and Solution of Layered Queueing
  Networks," _IEEE Transactions on Software Engineering_, 2009.
  [layeredqueues.org](http://www.layeredqueues.org)

LQNs model call graphs with nested synchronous calls and compute bounds on
resource contention. Focus is response time and throughput rather than
structural properties like depth or span count.

## Graph-theoretic topology analysis

- Du et al., "A Microservice Graph Generator with Production
  Characteristics," _ICS_, 2025. Categorises dependency graphs from Alibaba
  and Meta into topological types and generates synthetic graphs matching
  production characteristics. Children set sizes range 1--10 (Alibaba) to up
  to 50 (Meta).
  [arXiv](https://arxiv.org/html/2412.19083v1)

- Baresi et al., "Graph-based and Scenario-driven Microservice Analysis,
  Retrieval, and Testing," _Future Generation Computer Systems_, 2019. Extracts
  service dependency graphs from microservice code for analysis and testing.
  [ScienceDirect](https://www.sciencedirect.com/science/article/abs/pii/S0167739X19302614)

- "Network Analysis of Microservices: A Case Study on Alibaba Production
  Clusters," _ACM/SPEC ICPE Companion_, 2024. Applies network science methods
  to service dependency graphs.
  [ACM DL](https://dl.acm.org/doi/10.1145/3629527.3651842)

## Synthetic trace generation

- Palette: "Generating Representative Macrobenchmark Microservice Systems from
  Distributed Traces," _ACM SIGOPS APSys_, 2025. Builds a Graphical Causal
  Model from production traces that captures branching probabilities, execution
  order, and execution times, then generates synthetic systems. The closest
  work to motel's simulation approach. Key difference: Palette _learns_ its
  model from traces, while motel takes a hand-authored topology as input.
  [arXiv](https://arxiv.org/abs/2506.06448)

- MSTG: "A Flexible and Scalable Microservices Infrastructure Generator," 2024. Generates running microservice deployments from a configuration file
  describing a topology. More about infrastructure simulation than trace
  generation, but the topology-to-system direction is the same.
  [arXiv](https://arxiv.org/pdf/2404.13665)

- Gan et al., "An Open-Source Benchmark Suite for Microservices and Their
  Hardware-Software Implications for Cloud and Edge Systems" (DeathStarBench),
  _ASPLOS_, 2019. Provides real microservice applications as benchmarks.
  Widely used as reference topologies for microservice research.
  [ACM DL](https://dl.acm.org/doi/10.1145/3297858.3304013) |
  [GitHub](https://github.com/delimitrou/DeathStarBench)

- Courageux-Sudan et al., "Automated Performance Prediction of Microservice
  Applications Using Simulation," _MASCOTS_, 2021. Uses SimGrid to predict
  microservice performance from a model. Focuses on response time rather than
  trace shape.
  [HAL](https://hal.science/hal-03389508v1/document)

## Commentary

The specific combination -- static worst-case structural bounds via DFS on a
topology model, combined with Monte Carlo simulation for percentile
distributions -- does not appear to have been formalised as a named technique.
The ingredients exist independently:

| Ingredient                                       | Established in                      |
| ------------------------------------------------ | ----------------------------------- |
| Graph-traversal worst-case bounds                | WCET analysis (Wilhelm et al. 2008) |
| Call graph modelling                             | LQNs (Franks et al. 2009)           |
| Model-based prediction                           | SPE (Smith & Williams 2002)         |
| Trace shape metrics (depth, fan-out, span count) | Alibaba/Meta studies (2021--2023)   |
| Synthetic trace generation from models           | Palette (2025), MSTG (2024)         |

The predictive direction (model to predicted trace shape) is notably less
explored than the observational direction (production traces to measured shape).

**Caveat:** this survey was conducted in February 2026 and may have missed
relevant work in formal methods or real-time systems venues.
