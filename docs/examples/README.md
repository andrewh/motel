# Example Topologies

Ready-to-use YAML topology files for motel. Each file is self-contained
and can be validated and run directly:

```sh
motel validate docs/examples/basic-topology.yaml
motel run --stdout --duration 5s docs/examples/basic-topology.yaml
```

## Files

| File | Description |
|------|-------------|
| `basic-topology.yaml` | Five-service topology with attributes, weighted status codes, and a scenario. Best starting point. |
| `traffic-patterns.yaml` | Minimal two-service topology for comparing traffic arrival models (uniform, diurnal, bursty). |
| `scenario-override.yaml` | Three overlapping scenarios with priority stacking, attribute overrides, and traffic rate changes. |
| `cascading-failure.yaml` | Timeout and retry through a three-tier chain during scenario-driven database degradation. |
| `circuit-breaker.yaml` | Scenario `add_calls`/`remove_calls` for circuit-breaker fallback patterns. |
| `conditional-calls.yaml` | Per-call probability, `on-error`/`on-success` conditions, and `count` fan-out. |
| `backpressure-queue.yaml` | Queue depth rejection, circuit breaker trips, and backpressure duration amplification. |

## Further reading

- [Getting started tutorial](../../docs/tutorials/getting-started.md)
- [Topology DSL reference](../../cmd/motel/README.md)
- [Modelling your services](../how-to/model-your-services.md)
