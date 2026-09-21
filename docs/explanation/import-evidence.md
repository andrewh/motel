# Trace import evidence and limits

Span-based import emits capture-wide `import` metadata and operation-level
`import.latency` and `import.attributes` mappings. The parsed `synth.Config`
and `OperationConfig` retain these mappings as opaque informational values,
including unknown evidence fields. They are not validated as execution fields
or copied into a built topology or emitted attributes. A valid topology remains
runnable with insufficient, unfamiliar, or user-edited evidence.

## Own time

For each effective operation invocation, own time is elapsed span duration
minus the union of its effective downstream children's intervals. Intervals
are clipped to the parent's bounds; overlapping time is subtracted once.
Negative elapsed times become zero estimates. The existing emitter requires a
positive mean, so the rounded emitted mean has a 1 microsecond floor.

Same-operation continuation spans are folded by the existing call inference;
their downstream calls attach to the enclosing operation. Latency observations
refer to effective invocations; attribute observations include every source
span of that operation, including continuations.

An out-of-bounds or negative child interval, or negative parent duration,
records a timestamp anomaly. Known missing parents and timestamp anomalies
make fit evidence insufficient even if quantiles appear close. A capture may
omit children without leaving a broken parent reference: completeness remains
unknown and their unobserved time stays in the own-time estimate. Treat these
estimates as conditional on the capture, not as measurements of CPU time.

Call inference still chooses sequential or parallel style by observed votes.
Sequential generation sums child durations; parallel generation starts all
children together and takes their maximum. Staggered overlaps, mixed styles,
correlated calls, retries, async work, and varying repetition counts may not
survive this approximation. Total-latency assessment exposes discrepancies;
no new distribution or call-scheduling model is introduced.

## Fit policy

Each own-time and total-time assessment records observation count, modeled
count, observed and modeled median/p95 in nanoseconds, status, and reason codes.
Quantiles use the nearest-rank definition. Statuses are:

- `fit`: both quantiles meet tolerance, with adequate evidence.
- `poor_fit`: median or p95 differs by more than tolerance.
- `insufficient_evidence`: fewer than 100 observed or modeled samples, known
  missing parents, timestamp anomalies, or bounded regeneration.

The per-quantile tolerance is the greater of **20% of the observed value or
1 ms**. These are initial engineering thresholds, not statistical confidence
intervals. At 100 observations, only about five observations describe the
upper 5% tail; fewer samples are explicitly insufficient. The absolute floor
avoids false precision for very short spans. For sub-millisecond systems,
`fit` under this policy may be too permissive for your alert requirements.

Own-time assessment samples the actual parsed duration string after rounding
and uses the existing normal sampler with zero clamping. Total-time assessment
runs `synth.GenerateTraces` on the emitted execution model with seed 253 and
2,048 traces, collecting elapsed spans through its observer API. Rarely visited
operations may have insufficient modeled samples even in a large capture.
The same fixed seed is used for 2,048 own-time draws per operation. The capture
records these settings. No fit is promised to generalize to future traffic.

Validation cases establish the policy's practical meaning:

| Case | Expected assessment |
| --- | --- |
| One 30 ms parent with a 10 ms child | Runnable 20 ms own-time estimate; insufficient evidence |
| 100 such invocations | Own time and regenerated 30 ms totals fit |
| Sequential or aligned parallel children | Subtract union; restore original total |
| Children at 1–17 ms and 14–29 ms inside a 30 ms parent | Own-time fit; regenerated 18 ms total is a poor fit |
| Equal populations at 1 ms and 99 ms | Existing normal model is a poor fit |
| Positive duration below 1 microsecond | Emitted floor is assessed, rather than an unrounded estimate |
| Missing parent or out-of-bounds interval | Runnable estimate with insufficient evidence |

## Attributes and omissions

An operation retains a span attribute only when the same supported scalar,
including its type, occurs on **every** observed span of that operation.
Supported types are string, signed 64-bit integer, float64, and boolean.
Integers are decoded without passing through a float; integer-valued floats
retain explicit YAML float typing. Static attributes consume no random draws.

Each encountered key records presence count, unsupported count, status,
retained scalar type, and zero or more reasons: `partial_presence`,
`varying_value_or_type`, `unsupported_value`, or `reserved_engine_attribute`.
Arrays, objects, binary values, nulls, and unsupported source types are omitted.
Engine identity attributes (`synth.service`, `synth.operation`,
`synth.scenarios`) are regenerated by Motel and reported as omitted.

Resource `service.name` supplies service identity. Other resource keys are
counted under capture-wide `resource_omissions`, once per source span carrying
that resource key. Span attributes are never promoted to resource attributes.
Broader resource preservation and varying attribute models are deferred.

## Reports and defaults

`--report path.md` renders the same structured evidence saved in the topology;
the stderr summary counts operations by latency assessment. The report includes
attribute omissions and capture settings. Evidence records observed source
counts and the root observation window; traffic uses that window where possible
and otherwise defaults to 1/s. Existing integer traffic-rate formatting remains
an approximation. Inferred durations, calls, and probabilities remain editable
execution fields. Evidence is a record of the original import only.

Operation `import.inference` also retains error counts, per-target call presence
and occurrence counts, and parallel/sequential votes with the inferred style
and its default-or-observed basis. `requested_min_samples` records the normalized
`--min-traces` threshold. Structured reasons preserve low-sample and weak/mixed
call-style warnings; terminal confidence diagnostics use this same assessment.
The latency observation count supplies the parent count for call probabilities.
