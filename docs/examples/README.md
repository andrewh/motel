# Example Topologies

Ready-to-use YAML topology files for motel. Each file is self-contained
and can be validated, checked, and run directly:

```sh
motel validate docs/examples/basic-topology.yaml
motel check docs/examples/basic-topology.yaml
motel run --stdout --duration 5s docs/examples/basic-topology.yaml
```

## Files

| File | Description |
|------|-------------|
| `minimal.yaml` | Smallest valid topology. Single service, single operation. |
| `basic-topology.yaml` | Five-service topology with attributes, weighted status codes, and a scenario. Best starting point. |
| `traffic-patterns.yaml` | Minimal two-service topology for comparing traffic arrival models (uniform, diurnal, bursty). |
| `scenario-override.yaml` | Three overlapping scenarios with priority stacking, attribute overrides, and traffic rate changes. |
| `cascading-failure.yaml` | Timeout and retry through a three-tier chain during scenario-driven database degradation. |
| `circuit-breaker.yaml` | Scenario `add_calls`/`remove_calls` for circuit-breaker fallback patterns. |
| `conditional-calls.yaml` | Per-call probability, `on-error`/`on-success` conditions, and `count` fan-out. |
| `async-calls.yaml` | Async fire-and-forget calls with trace propagation for audit logging and notification dispatch. |
| `span-events.yaml` | Span events emitted at an offset within operations (cache misses, query starts). |
| `span-links.yaml` | Cross-trace span links between producer and consumer, modelling a message queue. |
| `producer-consumer.yaml` | Messaging `PRODUCER`/`CONSUMER` span kinds: a `producer: true` publish span paired with an async consumer that links back across traces. |
| `attribute-placement.yaml` | Resource attributes vs span attributes on the same service. |
| `resource-attributes.yaml` | Per-service resource attributes (`deployment.environment`, `service.version`, etc.). |
| `topology-driven-metrics.yaml` | All four metric instrument types at both service and operation level. |
| `topology-driven-logs.yaml` | Log templates with conditions, probability, timing anchors, and scenario log overrides. |
| `backpressure-queue.yaml` | Queue depth rejection, circuit breaker trips, and backpressure duration amplification. |
| `internal-gateway.yaml` | Internal gateway pattern with async trace propagation through a service mesh. |
| `ottl-transforms.yaml` | Messy, realistic attributes for practising OTTL transformations. |
| `stress-test.yaml` | High-volume bursty topology for stress-testing collector queues. |
| `tail-sampling-test.yaml` | Mix of normal, slow, error, and VIP traces for testing tail sampling policies. |
| `alibaba-call-graph.yaml` | Alibaba-style call graph modelling published trace study characteristics (depth 6, fan-out 10, 16% repeated calls). |
| `meta-wide-fanout.yaml` | Meta-style wide fan-out workflow (50 children at the aggregation tier) from published trace studies. |

## Subdirectories

| Directory | Description |
|-----------|-------------|
| [`dsb/`](dsb/README.md) | DeathStarBench microservice topologies (Social Network, Hotel Reservation). |

## Further reading

- [Getting started tutorial](../tutorials/getting-started.md)
- [Topology DSL reference](../../cmd/motel/README.md)
- [Modelling your services](../how-to/model-your-services.md)
