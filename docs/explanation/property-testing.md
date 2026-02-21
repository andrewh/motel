# Property Testing in motel

This document explains how motel uses property-based testing, why we chose the
tools we did, what we test, and how to extend the test suite.

## Why property testing

Traditional unit tests check specific examples: given this input, expect that
output. Property tests instead describe invariants that must hold for *any*
valid input, then let the framework generate thousands of random inputs to find
violations.

For motel, this matters because the synth engine has a large input space
(arbitrary topologies, traffic patterns, scenario overlays, circuit breaker
configurations) and many interacting components. Hand-written examples can't
cover the combinatorial surface. Property tests have already found bugs that
example-based tests missed.

## Why rapid

We use [pgregory.net/rapid](https://github.com/flyingmutant/rapid) (v1.2.0+).

Reasons for choosing rapid over alternatives (notably `testing/quick` and
`leanovate/gopter`):

- **Integrated shrinking.** When rapid finds a failing input, it automatically
  reduces it to a minimal reproducing case. `testing/quick` doesn't shrink at
  all; `gopter` requires manual shrinking setup.
- **Composable generators.** `rapid.Custom` wraps any generator function into a
  composable value. `rapid.StringMatching(regex)` generates strings from a
  regular expression. These make it easy to build generators for domain types
  like rate strings (`"100/s"`) and duration strings (`"50ms"`).
- **State machine testing.** `t.Repeat` drives random action sequences against
  stateful systems. We use this for the circuit breaker, where the interesting
  bugs are in state transitions, not individual operations.
- **`MakeFuzz` bridge.** `rapid.MakeFuzz` converts any property test into a
  `go test -fuzz` target with no boilerplate. This gives us coverage-guided
  fuzzing for free on top of the random testing.
- **Good failure output.** Failures include the exact draw sequence, making
  reproduction trivial — copy the logged seed and the test replays identically.

## What we test

### Synth engine (`pkg/synth/property_test.go`)

**Topology conformance (7 tests).** Every span produced by the engine
references a valid operation in the topology. Refs are correctly formatted as
`service.operation`. Roots are complete, sorted, and not called by other
operations. Cycle detection rejects invalid configs. Call targets resolve to
real operations.

**Span structure (4 tests).** Children start after their parent, parents end
after their children, durations are positive. Root spans are SERVER kind,
non-root spans are CLIENT kind. All spans in a trace share the same trace ID.

**Call graph (1 test).** Every parent-child span pair corresponds to a valid
call edge in the topology.

**Stats consistency (2 tests).** The exporter span count matches
`Stats.Spans`. Error count never exceeds total span count.

**Error cascading (1 test).** If a child span has an error, its parent also
has an error.

**Scenarios (5 tests).** `ActiveScenarios` returns only scenarios whose window
contains the current time, sorted by priority, with stable ordering.
`ResolveOverrides` merges correctly (last-defined wins), preserves earlier
fields when later scenarios only partially override, doesn't mutate input
scenarios, and includes all refs.

**Traffic resolution (1 test).** `ResolveTraffic` returns the traffic pattern
from the highest-priority scenario.

**Engine with overrides (2 tests).** Duration overrides change actual span
durations. Error rate override of 100% produces error spans.

**Attribute generators (6 tests).** StaticValue is constant, WeightedChoice
outputs are within the choice set, BoolValue returns booleans with correct
extreme behaviour, RangeValue stays within bounds with single-value identity,
SequenceValue is monotonic, NormalValue mean converges.

**Distributions (4 tests).** Samples are non-negative (clamping works), zero
stddev returns exact mean, sample mean converges to configured mean,
`ParseDistribution` round-trips correctly.

**Rate parsing (4 tests).** Valid rates parse and round-trip. Zero/negative
counts are rejected. Counts exceeding `MaxRateCount` are rejected. Per-second
unit is preserved.

**Config validation (5 tests).** Valid generated configs are accepted. Missing
services, missing traffic rate, bad call targets, and bad durations are all
rejected.

**Traffic patterns (5 tests).** Uniform rate is constant. Diurnal rate stays
within trough/peak bounds. Bursty rate alternates between burst and base.
All patterns return non-negative rates. Custom segment boundaries are
respected.

**Circuit breaker state machine (1 test).** Uses `t.Repeat` to drive random
sequences of success requests, failure requests, and time advances. A
simplified model independently tracks the expected circuit state
(Closed/Open/HalfOpen). After every action, the test checks that the real
`OperationState` matches the model, that open circuits reject within cooldown,
and that active request counts are never negative.

### Semantic conventions (`pkg/semconv/property_test.go`)

**Registry indexing (9 tests).** Group and attribute lookups are consistent
with what was stored. Nonexistent keys return nil. Domains are sorted. Domain
group counts match input. Merge contains both registries without mutating
originals. Ref resolution inherits type and stability from the definition while
allowing brief overrides. Load produces the correct group count.

### Import pipeline (`pkg/synth/traceimport/property_test.go`)

**Tree construction (4 tests).** All input spans appear in the output trees.
Each tree has a single root. Every span is reachable from the root. No cycles
exist.

**Stats collection (5 tests).** Operation counts match input. Error counts
are bounded by total counts. Duration lists match span counts and are positive.
Call counts match tree children.

**Duration arithmetic (4 tests).** Computed mean is within min/max. Combining
a distribution with itself is idempotent. Uniform distributions (zero stddev)
are preserved. Standard deviation is non-negative and zero for uniform inputs.

**Marshal round-trip (1 test).** Generated YAML passes `LoadConfig` and
`ValidateConfig`. All services from the input appear in the output.

## Bugs found

The initial property testing run found two bugs in `MarshalConfig`:

1. **Rate overflow.** When input traces are nanoseconds apart, the computed
   rate exceeded the 10,000/s validator cap. Fixed by clamping to
   `MaxRateCount` before formatting.

2. **Fractional rate format.** Sub-1/s rates produced strings like `"0.20/s"`,
   which the integer-only rate parser rejected. Fixed by converting to
   per-minute format (`"12/m"`) with a `"1/m"` floor for extremely low rates.

Both bugs would have caused `motel import` to produce topologies that
`motel run` then rejected — a round-trip failure that no existing
example-based test had caught.

## Generators

The property tests use composable generators defined at the top of each test
file. Key generators:

- **`genSimpleConfig`** — Produces a valid `*Config` with 1-4 services, 1-3
  operations each, a DAG of calls (no cycles), random durations, error rates,
  and a traffic rate. Wrapped as `rapid.Custom(genSimpleConfig)` for use in
  combinators.
- **`genDurationString`** — `rapid.StringMatching("[1-9][0-9]{0,2}ms")`.
  Generates valid duration strings directly from the grammar.
- **`genRateString`** — `rapid.StringMatching("[1-9][0-9]{0,3}/[smh]")`.
  Generates valid rate strings.
- **`genErrorRateString`** — `rapid.StringMatching("[1-9][0-9]?%")`.
- **`genScenario` / `genScenarioList`** — Generates scenarios with random
  activation windows, priorities, and partial overrides for given operation
  refs.
- **`genMultiTraceSpans`** — Generates realistic span data for the import
  pipeline, with multiple traces, services, and parent-child relationships.

To add a new property test, either reuse an existing generator or build a new
one with `rapid.Custom`. The pattern is always the same: generate valid input,
run the code, check an invariant.

## Fuzz targets

Five fuzz targets wrap property tests via `rapid.MakeFuzz`:

| Target | Package | What it exercises |
|---|---|---|
| `FuzzValidateConfig` | `pkg/synth` | Config generation and validation |
| `FuzzBuildTopology` | `pkg/synth` | Topology building and ref correctness |
| `FuzzParseDistribution` | `pkg/synth` | Distribution parse/format round-trip |
| `FuzzParseRate` | `pkg/synth` | Rate parsing with regex-generated strings |
| `FuzzMarshalRoundTrip` | `pkg/synth/traceimport` | Full import pipeline round-trip |

### Running fuzz targets

During normal `make test`, corpus entries in `testdata/fuzz/` replay
automatically. This catches regressions without any extra setup.

To run extended fuzzing (recommended after changing parsers, validators, or the
import pipeline):

```bash
# Run a single target for 5 minutes
go test ./pkg/synth/ -fuzz=FuzzParseRate -fuzztime=5m

# Run all synth targets in parallel
go test ./pkg/synth/ -fuzz=FuzzValidateConfig -fuzztime=5m &
go test ./pkg/synth/ -fuzz=FuzzBuildTopology -fuzztime=5m &
go test ./pkg/synth/ -fuzz=FuzzParseDistribution -fuzztime=5m &
go test ./pkg/synth/ -fuzz=FuzzParseRate -fuzztime=5m &
wait

# Run the import pipeline target separately (different package)
go test ./pkg/synth/traceimport/ -fuzz=FuzzMarshalRoundTrip -fuzztime=5m
```

### Committing corpus entries

Go's fuzz engine writes new interesting inputs to its build cache, not to
`testdata/fuzz/`. After a fuzzing run, copy them to the repo and commit:

```bash
# macOS — cache location
src=~/Library/Caches/go-build/fuzz/github.com/andrewh/motel

# Linux — cache location
# src=~/.cache/go-build/fuzz/github.com/andrewh/motel

# Copy new entries (cp -n skips existing files)
for target in FuzzValidateConfig FuzzBuildTopology FuzzParseDistribution FuzzParseRate; do
  cp -n "$src/pkg/synth/$target"/* "pkg/synth/testdata/fuzz/$target/"
done
cp -n "$src/pkg/synth/traceimport/FuzzMarshalRoundTrip"/* \
  "pkg/synth/traceimport/testdata/fuzz/FuzzMarshalRoundTrip/"

# Verify tests still pass with new corpus
make test

# Commit
git add pkg/synth/testdata/fuzz/ pkg/synth/traceimport/testdata/fuzz/
git commit -m "Add fuzz corpus entries"
```

A failure during fuzzing means the fuzzer found a bug. The failing input is
saved to `testdata/fuzz/` automatically by `go test`. Fix the bug, confirm the
corpus entry now passes, and commit both the fix and the corpus entry.
