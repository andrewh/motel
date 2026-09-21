# Import traces, save a topology, and change endpoint latency

This walkthrough imports a supplied capture, reuses its topology, and changes
one endpoint to cross a 50 ms alert threshold. Run commands from the repository
root after `make build`.

## Capture provenance

[import-capture.jsonl](../examples/import-capture.jsonl) is an authored synthetic
stdouttrace-format fixture for this walkthrough, not a production capture. It
contains 100 complete traces, one per second starting at 2024-01-01T00:00:00Z.
Every trace has a 30 ms `api.GET /users` span and a 10 ms `db.query` child
starting 10 ms after its parent. There are no errors, sampling gaps, variable
latencies, or asynchronous calls. Its constant span attributes include a string,
integer, boolean, and float. Resource `host.name` is deliberately omitted and
counted in the import evidence. The fixture contains no user data.

These deliberately simple timings establish the import/save/edit workflow.
They do not establish fit for a real system's latency distribution. Separate
validation cases exercise insufficient evidence, overlapping calls, bimodality,
rounding, and timestamp anomalies.

## Import and save

```sh
build/motel import docs/examples/import-capture.jsonl \
  --report /tmp/import-report.md > /tmp/imported.yaml
build/motel validate /tmp/imported.yaml
```

The terminal summary reports 100 traces, 200 spans, and two latency fits.
The saved topology sets the API's **own time** to `20ms` and the database's to
`10ms`. Generation adds downstream time, restoring a 30 ms endpoint duration.
The report and YAML contain the same evidence; the report does not reassess the
saved file. `--min-traces` controls a warning, not the fit-assessment minimum.

For your own capture, substitute its path in the same command. Auto-detection
accepts OTLP JSON, Jaeger/Tempo JSON, and stdouttrace JSON lines. Use `--format`
if needed. Exporting from a tracing backend is outside this walkthrough.
Meta summary imports have no individual span observations and do not receive
this span-based fit or attribute assessment.

## Reuse and inspect

```sh
build/motel run --stdout --duration 3s --seed 253 /tmp/imported.yaml \
  > /tmp/baseline.jsonl
```

Inspect emitted durations and typed attributes using Python's standard library:

```sh
python3 - /tmp/baseline.jsonl <<'PY'
import datetime, json, sys
for line in open(sys.argv[1]):
    span = json.loads(line)
    if span['Name'] != 'GET /users':
        continue
    start = datetime.datetime.fromisoformat(span['StartTime'].replace('Z', '+00:00'))
    end = datetime.datetime.fromisoformat(span['EndTime'].replace('Z', '+00:00'))
    print(round((end - start).total_seconds() * 1000), 'ms')
    print({a['Key']: a['Value'] for a in span['Attributes']
           if not a['Key'].startswith('synth.')})
PY
```

Each API span should be 30 ms. Its attributes retain `http.request.method`
as `STRING`, `http.response.body.size` as `INT64`, `response.cached` as `BOOL`,
and `response.ratio` as `FLOAT64`. Import metadata is not a telemetry attribute.

## Change one endpoint

Copy `/tmp/imported.yaml` to `/tmp/slow.yaml`. Under
`services.api.operations.GET /users`, change only `duration: 20ms` to
`duration: 100ms`. The existing `100ms +/- 10ms` syntax can also add variation;
use the fixed duration for this exact threshold demonstration.

```sh
build/motel run --stdout --duration 3s --seed 253 /tmp/slow.yaml \
  > /tmp/slow.jsonl
```

Repeat the inspection command with `/tmp/slow.jsonl`. The API spans now last
110 ms (100 ms own time plus the unchanged 10 ms query), crossing 50 ms while
retaining their attributes. To exercise Motel's slow-span log threshold:

```sh
build/motel run --stdout --signals traces,logs --slow-threshold 50ms \
  --duration 3s --seed 253 /tmp/slow.yaml > /tmp/slow-signals.jsonl
```

The API spans emit slow-span logs; database spans remain below the threshold.
Mixed trace/log output is for inspection and must not be reimported as traces.
You can instead send telemetry to your collector with the existing `--endpoint`
flag to exercise a backend alert rule; backend configuration is not covered here.

The `import` mappings still describe the **original capture**, including its
30 ms endpoint baseline. They have no execution effect. Motel does not track
edits, detect stale evidence, or reassess modified topologies.

See [import evidence and limitations](../explanation/import-evidence.md) before
interpreting a fit as evidence about your production system.

## Recorded validation

Validated locally on 2026-09-21 for issue 253: `make test` (including fuzz-corpus
replay), `make lint`, and `make build` passed. The CLI imported the supplied
100-trace/200-span capture with two fits, saved its report, and validated the
saved topology. Three baseline endpoint spans were 30 ms; three edited spans
were 110 ms with retained integer, boolean, and float attributes. The edited
mixed-signal run emitted three slow-span logs at the 50 ms threshold. Standards
and specification reviews had no remaining findings after fixes. No external
collector or backend alert rule was used in this validation.
