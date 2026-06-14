# Palette Trace-Derived Benchmark Generation

Issue 195 tracks Palette as a research comparison for `motel import`.
Palette is close to motel because it learns from distributed traces, but its
target artifact is different: it generates deployable macrobenchmark
microservice systems through Blueprint, while motel imports traces into
topology YAML for synthetic OpenTelemetry generation.

## Evidence Checked

- Issue: <https://github.com/andrewh/motel/issues/195>
- Paper: Anand, Stolet, Mace, and Kaufmann, "Generating representative
  macrobenchmark microservice systems from distributed traces with Palette",
  arXiv:2506.06448, submitted 2025-06-06.
  <https://arxiv.org/abs/2506.06448>
- ACM DL entry: <https://dl.acm.org/doi/abs/10.1145/3725783.3764387>
- Blueprint repository: <https://github.com/Blueprint-uServices/blueprint>
- Source availability check on 2026-06-14: GitHub repository searches for the
  paper title, arXiv id, and Palette trace benchmark terms returned no
  Palette-specific repository. Searches inside `Blueprint-uServices/blueprint`
  for Palette, GCM, PFA, and the arXiv id also returned no results. Treat this
  as "not found", not proof that source will never appear.

## Short Answer

Palette should stay as a research note for now, not a direct dependency or
benchmark experiment. The paper is useful because it names several weaknesses
in simple trace-to-model importers, especially caller-conditioned latency,
probabilistic execution states, and richer sequential/concurrent behavior. A
bridge or shared benchmark should wait until Palette publishes source, schemas,
or representative intermediate artifacts.

The independently useful motel work is smaller:

- detect or expose caller-conditioned latency during import
- infer variable span attributes such as request or response size
- preserve more execution ordering evidence than a single operation-level
  `call_style`
- report when imported child-call probabilities look like mutually exclusive
  choices rather than independent calls

Blueprint is worth documenting as the deployment boundary, not as a second
trace-inference system. It matters when asking what Palette can generate or
what a future benchmark experiment would run, but it does not change the
near-term `motel import` recommendations unless Palette publishes
Blueprint-facing artifacts.

## Palette Model

Palette's system topology has three layers:

| Layer | What Palette models | Why it matters |
| ----- | ------------------- | -------------- |
| Directed graph | Services, APIs, and caller-callee edges, including local and remote calls | This is the part closest to motel's service/operation/call graph |
| Probabilistic Finite Automaton | Per-API execution states, state-transition probabilities, and states that issue one or more concurrent calls | This captures sequencing, branching, choices, and concurrency more precisely than a single call list |
| Graphical Causal Model | Per-property causal graph, such as latency or payload size, with equations fitted from traces | This conditions generated behavior on observed causal relationships instead of independent aggregate sampling |

The paper's latency equations distinguish probability, sequential calls,
concurrent calls, and choices. The PFA supplies the execution behavior that
chooses which equation shape applies. Palette then compiles each API's PFA and
GCM into generated benchmark code and uses Blueprint for deployable
infrastructure.

Palette also supports interventions at four levels:

| Stage | Palette intervention | motel analogue |
| ----- | -------------------- | -------------- |
| Trace processing | Filter traces or add observed dimensions, such as payload size | Import options and future inference passes |
| Topology | Add, remove, or change graph vertices and edges | Edit topology YAML or apply scenarios |
| Specification | Change how a property is realized, such as sleep vs CPU work for latency | Not modeled; motel emits telemetry, not running services |
| Instantiation | Modify Blueprint IR, such as swapping RPC frameworks | Out of scope unless motel exports to a runtime generator |

## Blueprint Boundary

Blueprint describes itself as an extensible compiler for microservice
applications and as a way to run, reconfigure, and prototype benchmark
applications. In Palette, that makes Blueprint the concrete system-generation
target: Palette learns a topology model from traces, lowers it into generated
application specifications, and relies on Blueprint for deployable
infrastructure and runtime components.

That boundary is important for motel:

- `motel import` produces topology YAML for telemetry generation, not a
  runnable microservice application.
- A motel-to-Blueprint bridge would be a product-direction change toward
  benchmark system generation, not just an importer enhancement.
- A Palette-to-motel bridge would be more plausible at Palette's intermediate
  topology, PFA, GCM, or generated-spec layer than at Blueprint's compiled
  application layer.
- A Blueprint comparison becomes actionable only when Palette publishes
  generated Blueprint specs, an intermediate IR, or example generated systems.

For now, Blueprint should be referenced as Palette's deployment substrate and
as a possible future benchmark execution target. It should not pull the current
research issue into a full Blueprint survey.

## Current `motel import` Model

Generic `motel import` currently parses stdouttrace, OTLP JSON, or Jaeger JSON,
builds trace trees, collects per-operation statistics, and marshals topology
YAML. The imported topology preserves:

- service and operation names
- mean and standard deviation of operation span duration
- operation error rate
- downstream call probability
- one operation-level sequential vs parallel call-style vote
- service-level constant attributes
- root trace arrival rate when enough timestamps are present

The Meta summary path imports `parent-data.csv.gz` directly. It weights rows by
`num_calls`, estimates error rate from `num_returning_calls`, maps
`children_set` to calls, uses `concurrency_rate` to vote for parallel vs
sequential call style, and preserves upstream ingress ids as resource
attributes. Because the public Meta file has isolated parent rows rather than
trace ids, it cannot reconstruct complete multi-hop workflows.

## Comparison

| Question | Palette | motel today | Implication |
| -------- | ------- | ----------- | ----------- |
| End product | Deployable benchmark system generated through Blueprint | Topology YAML that emits synthetic telemetry | A direct import/export bridge is only useful at the model layer, not at runtime, unless motel gains benchmark generation |
| Graph model | Service/API graph with local and remote edges | Service/operation graph with downstream calls | Broadly compatible |
| Branch probability | PFA transition probabilities and per-call probability nodes | Independent per-call probability on each call | motel can represent many call probabilities but not explicit mutually exclusive choice groups |
| Execution order | PFA states and transitions, including concurrent call states | One operation-level `call_style` of `parallel` or `sequential` | motel loses mixed patterns inside one operation |
| Caller-conditioned latency | GCM can condition downstream behavior on upstream causal context | Each operation has one aggregate duration distribution | Import may smear distinct caller effects into one callee operation |
| Payload or request size | GCM can model additional properties when traces contain them | Attribute generators can emit ranges and distributions, but import does not infer them yet | Variable attribute import is a good near-term fit |
| Runtime feedback | Generated code measures live behavior and samples from causal equations | Engine samples configured distributions; backpressure/circuit breakers are explicit DSL state | GCM runtime is a large conceptual addition, not an import tweak |
| Intervention support | Primitive edits to trace processing, topology, specification, and Blueprint IR | Manual topology edits plus scenarios | Docs should compare concepts; benchmark experiments need Palette artifacts |
| Blueprint role | Deployment substrate for generated benchmark applications | No equivalent runtime target; motel emits telemetry directly | Treat Blueprint as a future execution target, not a current import dependency |
| Source bridge | Depends on unpublished Palette source or IR | YAML topology is public and stable | Revisit only when Palette publishes schemas or examples |

## Implementation Implications

### Caller-conditioned latency

Palette calls out a failure mode in simple statistical models: a callee's mean
latency can differ by caller, and aggregating the callee into one duration
distribution loses that context. motel currently has that limitation because
`OpStats` is keyed by service and operation only.

A small useful improvement would be an import diagnostic that records child
duration distributions by parent ref and warns when the same child operation
has materially different caller-conditioned distributions. That keeps the DSL
stable while telling users when the inferred topology is hiding an important
dependency. A larger option is to let import split operations by caller when
the distributions are clearly distinct, but that changes topology shape and
needs careful naming.

### Probabilistic execution states

motel's current calls are sampled independently. That is enough for optional
calls and fan-out, but it cannot say "choose exactly one of these states" or
"run this parallel group, then maybe this sequential group" within a single
operation. Palette's PFA handles that naturally.

The least invasive motel step is to enhance import diagnostics so it can flag
when observed children sets look like alternatives. A DSL-level solution would
need explicit call groups or choice groups. That is a real topology extension,
not just an importer enhancement.

### Sequential and concurrent dependency behavior

`call_style` is a single operation-level majority vote. Palette's PFA can
represent mixed execution within one API: different states can issue different
calls, and a state can issue concurrent calls before transitioning.

For motel, this suggests recording more evidence during import:

- count distinct children sets per operation
- count sequential and parallel evidence per children set
- warn when one operation mixes strong sequential and parallel patterns

Those reports would help users decide whether the imported topology is good
enough or whether the service should be modeled manually.

### Request and payload-size modeling

motel already has attribute generators for weighted values, integer ranges,
normal distributions, and booleans. Import currently keeps only service-level
constant attributes. Palette's payload-size discussion maps cleanly to a future
import pass that infers operation-level attribute generators for known numeric
span attributes, such as `http.request.body.size` and
`http.response.body.size`.

This is a good near-term import improvement because it uses existing YAML
surface area and does not require GCM semantics.

### Benchmark experiment with observability-platform

Do not start a shared benchmark experiment yet. Without Palette source,
schemas, or intermediate topology artifacts, motel can only compare against the
paper conceptually. A useful experiment becomes possible if Palette publishes:

- a trace-to-topology intermediate representation
- generated Blueprint specs
- example trace inputs and generated systems
- validation metrics for generated traces

At that point the most useful benchmark would compare trace-shape metrics and
telemetry behavior:

- import the same trace sample into motel
- generate or obtain Palette's output for the same sample
- compare depth, fan-out, span count, duration distributions, error behavior,
  and any payload-size attributes
- use `motel check` and backend queries against emitted telemetry as the common
  observability-facing comparison

## Decision

Keep the Palette comparison in docs until public artifacts appear. Do not add a
Palette dependency, generated code path, or Blueprint bridge now.

The most valuable next motel work is import quality, in this order:

1. Add caller-conditioned latency diagnostics.
2. Infer variable operation attributes for common request and payload-size
   fields.
3. Add children-set and mixed call-style diagnostics.
4. Revisit DSL call groups or choice groups if import evidence shows repeated
   need across real traces.
5. Revisit a Palette bridge only after source, schemas, or intermediate
   artifacts are public.
