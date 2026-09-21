# Importing traces into reusable topologies

This note preserves the design discussion preceding issue 253. Its status and
implementation findings below describe that earlier point in time. For the
implemented behavior, see [import evidence](../explanation/import-evidence.md)
and the [import/save/edit walkthrough](../how-to/import-and-change-latency.md).

Status: design discussion in progress. These are agreed directions, not a
description of completed functionality or an approved implementation plan.

## Agreed direction

Prioritize adoption through importing traces, deriving a topology, saving it,
and reusing it to generate modified synthetic traffic.

The first demonstration should slow one endpoint and regenerate telemetry.
This makes the value of an editable topology concrete beyond replaying a
capture.

Initially, payload scope means metadata already present in traces, such as
sizes and content types. Captured request or response bodies and actual
application message exchange are outside this initial scope.

Import should produce a usable topology alongside a report distinguishing
observed facts, inferred relationships, and defaults. Missing services,
sampling, and absent attributes limit what a capture can establish; the
report should make those limitations understandable without implying that
the complete system was observed.

The first deliverable should preserve attributes that are constant within
each operation, report omitted metadata, and demonstrate editing the saved
topology. Modeling varying attribute distributions is deferred.

Preserve constant scalar attributes with their original types on operations.
Report unsupported values and omitted resource metadata structurally. Broader
resource-metadata preservation is deferred.

The walkthrough should start with a supplied example capture and support the
same workflow with a user's exported trace file. Backend-specific trace
acquisition is outside this first deliverable.

An attribute is constant only when it is present with the same value on every
observed span of an operation. Otherwise, report its coverage and omission.

All import assessments and explanatory data must be represented structurally
in the saved topology, rather than existing only in prose or YAML comments.
For example, insufficient evidence can be represented by
`import_status: insufficient_evidence`. Place operation-specific evidence under
an operation-level `import` mapping, with separate assessments for latency and
attributes so that their evidence statuses remain distinguishable.
Provide a brief terminal summary and an optional saved Markdown import report
derived from the same structured data alongside the directly runnable topology.

Import metadata describes the original import. Motel is not responsible for
subsequent user edits to topology files: this work does not add edit tracking,
staleness detection, or automatic reassessment of that metadata.

Place capture-wide evidence in a top-level `import` mapping, including trace
counts, missing-parent findings, and capture-wide omissions. Import metadata
is informational and must not affect generated telemetry. Existing execution
fields determine traffic; insufficient evidence does not prevent running an
otherwise valid topology.

Latency correctness is a prerequisite for the adoption walkthrough. The
baseline should be proportionate to the original import, while latency must
remain independently configurable for experiments such as crossing an alert
threshold. The precise meaning and acceptance criteria for "proportionate"
remain open; this does not imply exact replay of the source capture.

Use Motel's existing fixed-duration and variation syntax for user-controlled
latency changes. No additional latency configuration mechanism is needed for
this workflow.

The first statistical improvement should estimate latency distributions from
repeated observations, accounting for downstream time when deriving an
operation's own processing time. Fit the existing duration model first and
report where it poorly represents the observations. Additional distribution
types require evidence from that assessment and are deferred. Describe the
result as an estimate from the capture, not the uniquely correct distribution
of the original system.

Assess typical latency and the slow tail by comparing observed and modeled
median and p95. When there are too few observations to assess fit, still
produce an editable topology, but record an insufficient-evidence status and
the observation count. The sample requirements and comparison tolerances have
not yet been selected.

Assess regenerated total endpoint latency as well as estimated own processing
time. Own-time fit alone does not establish that the endpoint latency seen in
traces, and used for alert thresholds, remains representative.

## Recommended next task

Improve import fidelity and structured evidence for saved, editable topologies.

1. Investigate and correct the translation from observed span elapsed times to
   operation own-time estimates, including overlapping downstream calls and
   incomplete evidence. Assess the actual emitted duration model, including
   rounding and nonnegative sampling behavior.
2. Assess observed versus modeled median and p95, covering own-time estimates
   and regenerated total endpoint latency. Establish sample requirements and
   tolerances from explicit validation cases; emit insufficient-evidence
   statuses where assessment is not supported.
3. Preserve typed constant operation attributes and represent assessments,
   counts, omissions, and reasons in top-level and operation-level `import`
   mappings. Derive human-readable reports from this structured evidence.
4. Demonstrate a supplied capture being imported, saved, rerun, and edited to
   change an endpoint's latency using existing duration syntax. Verify the
   intended latency change and retained attributes, and document the capture's
   provenance and limits.

These steps describe the proposed delivery sequence. This discussion has not
implemented, tested, committed, or published any of these changes.

## Open decisions

- The specific reuse workflow and criteria for representative telemetry.
- Baseline latency fidelity criteria and how users apply experimental changes.
- The scope and acceptance criteria of the existing duration model's fit
  assessment, including limited samples and incomplete traces.
- The structured status vocabulary.
- A suitable example capture and the evidence needed to validate the workflow.

## Current implementation findings

The existing import demo already saves an inferred topology and generates
telemetry from it. The adoption work should build on that workflow.

The inferred operation representation in
`pkg/synth/traceimport/marshal.go` includes durations, error rates, call styles,
and calls, but no operation attributes. The import pipeline retains
service-wide constant attributes; metadata that differs between operations
does not currently become editable operation attributes in the topology.

`pkg/synth/traceimport/infer.go` returns topology YAML and source counts and
writes warnings through a diagnostic writer. It does not yet return the
structured evidence report described above.

Duration inference currently records full source span elapsed time in
`pkg/synth/traceimport/stats.go`, while `pkg/synth/plan.go` treats the operation
duration as own processing time and adds downstream time. This can inflate
regenerated parent durations. A focused investigation must establish an
appropriate inference rule and its limitations before the walkthrough claims
latency fidelity.

## Existing walkthroughs

- [Import demo](../demos/demo-import.md)
- [Import inference worked example](../explanation/import-pipeline/README.md)
