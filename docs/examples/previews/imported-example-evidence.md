# Trace import report

Imported 100 traces (200 spans): latency fit 2, poor fit 0, insufficient evidence 0. Evidence describes the original import.

| Service | Operation | Own-time assessment | Total-time assessment | Observations |
| --- | --- | --- | --- | --- |
| api | GET /users | fit | fit | 100 |
| db | query | fit | fit | 100 |

## Structured evidence

```yaml
requested_min_samples: 1
resource_omissions:
    host.name: 200
source_trace_count: 100
source_span_count: 200
missing_parent_count: 0
window_seconds: 99
traffic_rate_basis: observed_root_window
minimum_observations: 100
relative_tolerance: 0.2
absolute_tolerance_ns: 1000000
model_samples: 2048
model_span_budget: 100000
modeled_spans: 4096
modeled_traces: 2048
model_seed: 253
reasons:
    - capture_completeness_unknown
    - independent_operation_and_call_sampling
    - original_import_only
operations:
    api:
        GET /users:
            inference:
                error_count: 0
                calls:
                    db.query:
                        present_count: 100
                        occurrence_count: 100
                        reasons: []
                call_style:
                    parallel_votes: 0
                    sequential_votes: 0
                    inferred_style: parallel
                    basis: default
                    reasons: []
                reasons: []
            attributes:
                observation_count: 100
                import_status: preserved
                keys:
                    http.request.method:
                        present_count: 100
                        unsupported_count: 0
                        import_status: preserved
                        scalar_type: string
                        reasons: []
                    http.response.body.size:
                        present_count: 100
                        unsupported_count: 0
                        import_status: preserved
                        scalar_type: int64
                        reasons: []
                    response.cached:
                        present_count: 100
                        unsupported_count: 0
                        import_status: preserved
                        scalar_type: bool
                        reasons: []
                    response.ratio:
                        present_count: 100
                        unsupported_count: 0
                        import_status: preserved
                        scalar_type: float64
                        reasons: []
            latency:
                import_status: fit
                observation_count: 100
                timing_anomaly_count: 0
                own_time:
                    import_status: fit
                    observation_count: 100
                    modeled_count: 2048
                    observed:
                        median_ns: 2e+07
                        p95_ns: 2e+07
                    modeled:
                        median_ns: 2e+07
                        p95_ns: 2e+07
                    reasons: []
                total_time:
                    import_status: fit
                    observation_count: 100
                    modeled_count: 2048
                    observed:
                        median_ns: 3e+07
                        p95_ns: 3e+07
                    modeled:
                        median_ns: 3e+07
                        p95_ns: 3e+07
                    reasons: []
                reasons:
                    - own_time_subtracts_clipped_child_interval_union
                    - unobserved_children_remain_in_own_time
    db:
        query:
            inference:
                error_count: 0
                calls: {}
                call_style:
                    parallel_votes: 0
                    sequential_votes: 0
                    inferred_style: parallel
                    basis: default
                    reasons: []
                reasons: []
            attributes:
                observation_count: 100
                import_status: preserved
                keys:
                    db.system.name:
                        present_count: 100
                        unsupported_count: 0
                        import_status: preserved
                        scalar_type: string
                        reasons: []
            latency:
                import_status: fit
                observation_count: 100
                timing_anomaly_count: 0
                own_time:
                    import_status: fit
                    observation_count: 100
                    modeled_count: 2048
                    observed:
                        median_ns: 1e+07
                        p95_ns: 1e+07
                    modeled:
                        median_ns: 1e+07
                        p95_ns: 1e+07
                    reasons: []
                total_time:
                    import_status: fit
                    observation_count: 100
                    modeled_count: 2048
                    observed:
                        median_ns: 1e+07
                        p95_ns: 1e+07
                    modeled:
                        median_ns: 1e+07
                        p95_ns: 1e+07
                    reasons: []
                reasons:
                    - own_time_subtracts_clipped_child_interval_union
                    - unobserved_children_remain_in_own_time
```
