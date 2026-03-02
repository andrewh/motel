# motel: Span Events on Operations

*2026-03-02T16:00:00Z by Showboat 0.6.1*
<!-- showboat-id: span-events-demo -->

OpenTelemetry spans can carry events — timestamped annotations that mark something happening during the span's lifetime. Cache misses, query starts, connection pool acquisitions, message receipts: these are all things that happen *within* an operation, not as separate downstream calls. The `events` field on an operation models this pattern.

## The topology

An API service handles requests, emitting a cache miss event early in the span and a database query start event shortly after. It then makes a synchronous call to a database service, whose operation emits its own connection acquisition event.

```bash
cat docs/examples/span-events.yaml
```

```output
# Span events on operations
# Events are emitted via span.AddEvent() at startTime + delay.
# Useful for modelling cache misses, query starts, message receipts, etc.
# Run with: motel run --stdout span-events.yaml

version: 1

services:
  api:
    operations:
      GET /users:
        duration: 80ms +/- 20ms
        events:
          - name: cache.miss
            delay: 5ms
            attributes:
              cache.key:
                value: "user:*"
          - name: db.query.start
            delay: 10ms
            attributes:
              db.system:
                value: postgresql
              db.statement:
                value: "SELECT * FROM users"
        calls:
          - database.query

  database:
    operations:
      query:
        duration: 30ms +/- 10ms
        events:
          - name: connection.acquired
            delay: 2ms

traffic:
  rate: 5/s
```

Each event has a `name` and an optional `delay` (offset from span start time). Events can also carry `attributes` using the same attribute generators available on operations — `value`, `values`, `sequence`, `range`, `distribution`, and `probability`.

## Validation

```bash
motel validate docs/examples/span-events.yaml
```

```output
Configuration valid: 2 services, 1 root operation

To generate signals:
  motel run --stdout docs/examples/span-events.yaml

See https://github.com/andrewh/motel/tree/main/docs/examples for more examples.
```

Events are validated at load time. A missing name is an error:

```bash
cat > /tmp/bad-event.yaml << 'EOF'
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        events:
          - delay: 5ms
traffic:
  rate: 10/s
EOF
motel validate /tmp/bad-event.yaml 2>&1 | head -1
```

```output
Error: service "svc" operation "op": event[0]: name is required
```

Negative delays are also rejected:

```bash
cat > /tmp/bad-event2.yaml << 'EOF'
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        events:
          - name: test
            delay: -5ms
traffic:
  rate: 10/s
EOF
motel validate /tmp/bad-event2.yaml 2>&1 | head -1
```

```output
Error: service "svc" operation "op": event "test": delay must not be negative
```

## Events in the output

Each span's `Events` array contains the emitted events with their timestamps and attributes. The `GET /users` span carries two events; the `query` span carries one.

```bash
build/motel run --stdout --duration 200ms docs/examples/span-events.yaml 2>/dev/null | jq -rs '
  [.[] | select(.Events | length > 0) | {
    span: .Name,
    events: [.Events[] | .Name]
  }] | unique | .[]'
```

```output
{"span":"GET /users","events":["cache.miss","db.query.start"]}
{"span":"query","events":["connection.acquired"]}
```

## Event timing

Events are placed at `spanStartTime + delay`. The delay controls when within the span's lifetime the event appears. With a 5ms delay on `cache.miss` and a 10ms delay on `db.query.start`, the events always appear in that order, both before the span ends.

```bash
build/motel run --stdout --duration 200ms docs/examples/span-events.yaml 2>/dev/null | jq -rs '
  [.[] | select(.Name == "GET /users")] | .[0] |
  "cache.miss before db.query.start: \(
    (.Events | map(select(.Name == "cache.miss")) | .[0].Time) <
    (.Events | map(select(.Name == "db.query.start")) | .[0].Time)
  )",
  "both events before span end: \(
    (.Events | map(.Time) | max) < .EndTime
  )"'
```

```output
cache.miss before db.query.start: true
both events before span end: true
```

## Event attributes

Event attributes use the same generators as span attributes. The `cache.miss` event carries a `cache.key` attribute; the `db.query.start` event carries `db.system` and `db.statement`.

```bash
build/motel run --stdout --duration 200ms docs/examples/span-events.yaml 2>/dev/null | jq -rs '
  [.[] | select(.Name == "GET /users")] | .[0].Events |
  [.[] | {
    event: .Name,
    attributes: [(.Attributes // [])[] | "\(.Key)=\(.Value.Value)"]
  }] | .[]'
```

```output
{"event":"cache.miss","attributes":["cache.key=user:*"]}
{"event":"db.query.start","attributes":["db.system=postgresql","db.statement=SELECT * FROM users"]}
```

## Events without delay

The `delay` field is optional. Omitting it places the event at the span's start time — useful for recording something that happens at the beginning of the operation, like a message being received.

```bash
cat > /tmp/no-delay.yaml << 'EOF'
version: 1
services:
  consumer:
    operations:
      process:
        duration: 20ms +/- 5ms
        events:
          - name: message.received
            attributes:
              messaging.system:
                value: kafka
traffic:
  rate: 10/s
EOF
build/motel run --stdout --duration 200ms /tmp/no-delay.yaml 2>/dev/null | jq -rs '
  .[0] | "event at span start: \(.Events[0].Time == .StartTime)"'
```

```output
event at span start: true
```
