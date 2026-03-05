# motel: Emitting Traces Without a Topology File

*2026-03-05T07:51:15Z by Showboat 0.6.1*
<!-- showboat-id: f6468a6f-f097-4655-a56a-8a559d0357a2 -->

The `motel emit` command generates traces from command-line arguments, without writing a topology YAML file. This is useful for quick ad-hoc testing, scripting, and CI/CD pipelines where you need well-formed OTel signals with minimal setup.

## One trace, one span

At its simplest, `emit` takes a service name and operation name and produces a single trace on stdout.

```bash
motel emit --service api --operation "GET /health" --stdout 2>/dev/null | jq -r "\"operation: \(.Name)\", \"service: \(.Attributes[] | select(.Key == \"synth.service\") | .Value.Value)\", \"kind (2=SERVER): \(.SpanKind)\""
```

```output
operation: GET /health
service: api
kind (2=SERVER): 2
```

## Custom span duration

The default span duration is 100ms. Use `--duration` to set a specific value.

```bash
motel emit --service api --operation "GET /health" --duration 250ms --stdout 2>/dev/null | jq -r '"operation: \(.Name)", "has timestamps: \((.StartTime | length) > 0)"'

```

```output
operation: GET /health
has timestamps: true
```

## Attributes

Use `--attr key=value` (repeatable) to add custom string attributes to each span.

```bash
motel emit --service deploy --operation rollout --attr version=1.2.3 --attr env=prod --stdout 2>/dev/null | jq -r '[.Attributes[] | select(.Key | startswith("synth.") | not) | "\(.Key)=\(.Value.Value)"] | sort | .[]'

```

```output
env=prod
version=1.2.3
```

## Multiple traces

Use `--count` to generate multiple traces. The default rate is 10/s; override with `--rate`.

```bash
motel emit --service api --operation "GET /users" --count 5 --stdout 2>/dev/null | jq -rs '"traces: \([.[].SpanContext.TraceID] | unique | length)", "spans: \(length)"'

```

```output
traces: 5
spans: 5
```

## Error rate

Use `--error-rate` to inject synthetic errors. The value is a percentage (e.g. `50%`) or a decimal (e.g. `0.5`).

```bash
motel emit --service api --operation "GET /users" --error-rate 100% --count 3 --stdout 2>/dev/null | jq -rs '"total spans: \(length)", "errored: \([.[] | select(.Status.Code == "Error")] | length)"'

```

```output
total spans: 3
errored: 3
```

## Run statistics

Like `motel run`, `emit` prints a JSON stats summary to stderr when finished.

```bash
motel emit --service api --operation "GET /health" --count 3 --stdout 2>&1 >/dev/null | jq -r '"traces: \(.traces)", "spans: \(.spans)", "error_rate: \(.error_rate)"'

```

```output
traces: 3
spans: 3
error_rate: 0
```

For multi-service topologies, call graphs, scenarios, or sustained traffic generation, use `motel run` with a YAML topology file.
