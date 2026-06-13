# Swarm Testing for Topology Exploration

Swarm testing is an opt-in sampling strategy for `motel check`. The default
`random` strategy keeps sampled metrics close to the topology's configured
probabilities. The `swarm` strategy instead partitions the choice space so
sampled runs hit low-probability and retry-heavy corners more quickly.

Use swarm when you want to ask: "what structural bounds appear if several
unlikely choices happen together?" Keep random sampling when you want empirical
percentiles from the configured distribution.

```sh
motel check --sample-strategy swarm --samples 100 --seed 42 topology.yaml
```

## Choice Points

Swarm testing treats these engine decisions as boolean choice points:

| Kind | When it exists | `true` means | `false` means |
|---|---|---|---|
| Operation error | An operation has an effective `error_rate` between 0 and 1 | The operation's own error fires | The operation's own error does not fire |
| Call probability | An effective call has `probability` between 0 and 1 | The call is emitted | The call is skipped |
| Retry activation | An effective call has `retries` greater than 0 | Retry attempts are taken until the last attempt | The first attempt is final |

The model is built from the effective topology for each scenario set. That
means scenario `add_calls`, `remove_calls`, and error-rate overrides are applied
before choice points are enumerated.

Scenario activation itself is not a swarm choice point. `motel check` already
enumerates every distinct set of co-active scenarios and runs sampling for each
set separately.

## Strategy

For each sampled run, swarm testing creates a set of forced decisions and lets
all unforced choices use the normal engine RNG. The first run forces all choice
points enabled, covering error-conditioned calls and retry-heavy branches. The
second run forces operation errors off while probabilistic calls and retries are
enabled, covering healthy-path calls guarded by `condition: on-success`. The
third run forces all choice points disabled. Subsequent runs force individual
choice points in both directions while also fixing a random subset of other
points.

This gives two useful behaviours:

- A small sample can expose rare fan-out or span growth across both error and
  success paths that pure random sampling is unlikely to observe.
- Later samples still explore mixed partitions, such as one retry-heavy path
  activating while unrelated call probabilities vary normally.

The strategy never changes static analysis. `MaxDepth`, `MaxFanOut`, and
`MaxSpans` remain conservative upper bounds. Swarm only changes the sampled
observations and percentile summaries reported by `check`.

## Interpreting Results

Swarm percentiles are not production-frequency percentiles. They describe the
distribution of chosen partitions, not the probability distribution encoded in
the topology. This is useful for stress exploration and regression tests, but
random sampling is the better default for empirical threshold checks.

Retry activation can force retry control flow even when the child operation
would otherwise succeed. This models the retry path structurally: additional
attempt spans and retry counters appear, while child span error status remains
owned by the child operation's error decision.
