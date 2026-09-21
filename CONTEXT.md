# Motel

Motel models distributed systems to generate synthetic telemetry for testing
and developing observability pipelines.

## Language

**Topology**:
A reusable description of services, operations, calls, and traffic from which
Motel generates telemetry. A topology can be authored or inferred from traces.
_Avoid_: Config, schema

**Scenario**:
Time-windowed overrides layered on a topology to describe changes in simulated
behavior.

**Import report**:
An explanation of the evidence and limitations behind an inferred topology,
distinguishing observed facts, inferred relationships, and defaults.
