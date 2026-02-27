# motel: Asynchronous Fire-and-Forget Calls

*2026-02-26T20:59:33Z by Showboat 0.6.1*
<!-- showboat-id: e496cd03-79bb-41b8-84ed-19211402e856 -->

Not every downstream call needs a response. Event emission, audit logging, and notification dispatch are fire-and-forget: the caller sends the request and moves on without waiting for it to complete. The `async: true` field on a call models this pattern. The child span is created with the parent context (preserving trace propagation and the parent-child link), but the parent does not wait for the child to finish and does not inherit its errors. In traces, async children show up as spans that outlive their parent.

## The topology

A gateway receives requests and fans out to two services: an audit logger (async, fire-and-forget) and a backend processor (synchronous). The backend itself makes an async notification call and a synchronous database query. The audit log is slow (50ms) and occasionally errors, but this should never affect the gateway. The notification service is even slower (80ms) but the backend does not wait for it.

```bash
cat docs/examples/async-calls.yaml
```

```output
version: 1

services:
  gateway:
    operations:
      handle:
        duration: 15ms +/- 5ms
        calls:
          - target: audit.log
            async: true
          - target: backend.process

  audit:
    operations:
      log:
        duration: 50ms +/- 10ms
        error_rate: 1%

  backend:
    operations:
      process:
        duration: 25ms +/- 8ms
        calls:
          - target: notify.send
            async: true
          - target: db.query

  notify:
    operations:
      send:
        duration: 80ms +/- 20ms

  db:
    operations:
      query:
        duration: 10ms +/- 3ms

traffic:
  rate: 10/s
```

The two `async: true` calls — `audit.log` and `notify.send` — will produce child spans that extend past their parents. The synchronous calls (`backend.process` and `db.query`) work as before: the parent waits for them.

## Validation

```bash
motel validate docs/examples/async-calls.yaml
```

```output
Configuration valid: 5 services, 1 root operation

To generate signals:
  motel run --stdout docs/examples/async-calls.yaml

See https://github.com/andrewh/motel/tree/main/docs/examples for more examples.
```

motel models retries as caller-side behaviour: the caller waits for the response, observes a failure, and retries. An async caller has already moved on, so it cannot retry. (Real systems often have receiver-side retries — queue redelivery, SQS visibility timeouts — but those happen inside the target service, not as repeated calls from the parent.) The combination is rejected at validation time:

```bash
cat > /tmp/bad-async.yaml << 'EOF'
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        calls:
          - target: svc2.op2
            async: true
            retries: 1
  svc2:
    operations:
      op2:
        duration: 10ms
traffic:
  rate: 10/s
EOF
build/motel validate /tmp/bad-async.yaml 2>&1 | head -1
```

```output
Error: service "svc" operation "op": call "svc2.op2" async calls cannot have retries
```

## Structural analysis

Async calls still produce real spans in the trace, so `motel check` counts them in its structural analysis. The max-depth path includes async subtrees because they contribute to the total span count and call depth.

```bash
build/motel check docs/examples/async-calls.yaml 2>&1 | grep -E "max-depth:|max-spans:|path:"
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.handle → backend.process → notify.send
PASS  max-spans: 5 static worst-case, 5 observed/1000 samples (limit: 10000)
```

The deepest path is `gateway.handle → backend.process → notify.send` (depth 2) — the async notification chain is deeper than the synchronous database path (`backend.process → db.query`). All 5 spans (gateway, audit, backend, notify, db) are counted in max-spans.

## Trace timing: children outlive parents

The structural analysis confirms the topology is sound. Now let's look at actual span timing. In a synchronous call graph, the parent always wraps its children. With `async: true`, the child starts at the same time but runs independently — its end time extends past the parent. We can verify this by comparing span end times:

```bash
build/motel run --stdout --duration 2s docs/examples/async-calls.yaml 2>/dev/null | python3 -c '
import json, sys

spans = [json.loads(line) for line in sys.stdin]
by_id = {s["SpanContext"]["SpanID"]: s for s in spans}

async_outlives = 0
sync_wrapped = 0
for s in spans:
    pid = s["Parent"]["SpanID"]
    if pid == "0000000000000000":
        continue
    parent = by_id.get(pid)
    if not parent:
        continue
    if s["EndTime"] > parent["EndTime"]:
        async_outlives += 1
    else:
        sync_wrapped += 1

print(f"has async (child outlives parent): {async_outlives > 0}")
print(f"has sync (parent wraps child): {sync_wrapped > 0}")
'
```

```output
has async (child outlives parent): True
has sync (parent wraps child): True
```

The two async calls (`audit.log` and `notify.send`) produce child spans that end after their parents. The two synchronous calls (`backend.process` and `db.query`) are wrapped by their parents as usual.

## Error isolation

In synchronous call graphs, a child failure cascades to its parent — the parent span is marked as errored too. Async calls break this cascade: if `audit.log` errors, `gateway.handle` is unaffected. The error is recorded on the child span but does not propagate up.

```bash
cat > /tmp/async-errors.yaml << 'EOF'
version: 1
services:
  gateway:
    operations:
      handle:
        duration: 10ms
        calls:
          - target: audit.log
            async: true
  audit:
    operations:
      log:
        duration: 10ms
        error_rate: 50%
traffic:
  rate: 100/s
EOF
build/motel run --stdout --duration 2s /tmp/async-errors.yaml 2>/dev/null | python3 -c '
import json, sys

spans = [json.loads(line) for line in sys.stdin]
by_id = {s["SpanContext"]["SpanID"]: s for s in spans}

audit_errors = 0
gateway_cascaded = 0
for s in spans:
    if s["Name"] == "log" and s["Status"]["Code"] == "Error":
        audit_errors += 1
        pid = s["Parent"]["SpanID"]
        parent = by_id.get(pid)
        if parent and parent["Status"]["Code"] == "Error":
            gateway_cascaded += 1

print(f"audit.log has errors: {audit_errors > 0}")
print(f"errors cascaded to gateway: {gateway_cascaded > 0}")
'
```

```output
audit.log has errors: True
errors cascaded to gateway: False
```

`audit.log` spans have errors (expected with a 50% error rate), but none propagate to `gateway.handle` — the async boundary isolates the parent from the child's failures.
