# motel: Custom Semantic Conventions

*2026-02-21T13:58:49Z by Showboat 0.6.0*
<!-- showboat-id: 85352f86-8941-45ce-81fa-9bc3d56413bc -->

OpenTelemetry's semantic conventions define standard attribute names and types for common domains like HTTP, database, and messaging. But organisations often need attributes specific to their own services — a payment platform might track transaction types, a game studio might record match IDs. The `--semconv` flag lets you bring your own convention definitions without forking motel or recompiling.

## Defining custom conventions

A semantic convention directory mirrors the [Weaver](https://github.com/open-telemetry/weaver) registry layout: YAML files organised into subdirectories by domain. Each file defines one or more attribute groups.

```bash
mkdir -p /tmp/motel-semconv/payments && cat > /tmp/motel-semconv/payments/registry.yaml << 'EOF'
groups:
  - id: registry.payments
    type: attribute_group
    brief: Payment processing attributes.
    attributes:
      - id: payments.transaction_type
        type:
          members:
            - id: purchase
              value: purchase
              brief: Standard purchase.
              stability: stable
            - id: refund
              value: refund
              brief: Refund transaction.
              stability: stable
            - id: chargeback
              value: chargeback
              brief: Disputed charge.
              stability: stable
        brief: Type of payment transaction.
        examples: ["purchase", "refund"]
      - id: payments.amount_cents
        type: int
        brief: Transaction amount in cents.
        examples: [1999, 4500, 150]
      - id: payments.currency
        type: string
        brief: ISO 4217 currency code.
        examples: ["USD", "EUR", "GBP"]
      - id: payments.merchant_id
        type: string
        brief: Unique merchant identifier.
        examples: ["merch_abc123", "merch_def456"]
EOF
cat /tmp/motel-semconv/payments/registry.yaml
```

```output
groups:
  - id: registry.payments
    type: attribute_group
    brief: Payment processing attributes.
    attributes:
      - id: payments.transaction_type
        type:
          members:
            - id: purchase
              value: purchase
              brief: Standard purchase.
              stability: stable
            - id: refund
              value: refund
              brief: Refund transaction.
              stability: stable
            - id: chargeback
              value: chargeback
              brief: Disputed charge.
              stability: stable
        brief: Type of payment transaction.
        examples: ["purchase", "refund"]
      - id: payments.amount_cents
        type: int
        brief: Transaction amount in cents.
        examples: [1999, 4500, 150]
      - id: payments.currency
        type: string
        brief: ISO 4217 currency code.
        examples: ["USD", "EUR", "GBP"]
      - id: payments.merchant_id
        type: string
        brief: Unique merchant identifier.
        examples: ["merch_abc123", "merch_def456"]
```

This defines a `payments` domain with four attributes: an enum for transaction type, an integer for the amount, and strings for currency and merchant ID. The directory name (`payments/`) becomes the domain name used in topology files.

## Using custom conventions in a topology

Reference the custom domain in an operation's `domain` field. motel resolves the domain against the convention registry and generates attributes matching the defined types.

```bash
cat > /tmp/motel-payments.yaml << 'EOF'
version: 1
services:
  checkout:
    operations:
      process:
        duration: 50ms +/- 15ms
        error_rate: 0.5%
        domain: payments
        calls:
          - ledger.record
  ledger:
    operations:
      record:
        duration: 10ms +/- 3ms
        domain: payments
traffic:
  rate: 20/s
EOF
cat /tmp/motel-payments.yaml
```

```output
version: 1
services:
  checkout:
    operations:
      process:
        duration: 50ms +/- 15ms
        error_rate: 0.5%
        domain: payments
        calls:
          - ledger.record
  ledger:
    operations:
      record:
        duration: 10ms +/- 3ms
        domain: payments
traffic:
  rate: 20/s
```

## Validating with custom conventions

Pass `--semconv` to point motel at the convention directory. Without it, the `payments` domain would be silently unresolved — with it, motel knows what attributes to generate.

```bash
motel validate --semconv /tmp/motel-semconv /tmp/motel-payments.yaml
```

```output
Configuration valid: 2 services, 1 root operation

To generate signals:
  motel run --stdout /tmp/motel-payments.yaml

See https://github.com/andrewh/motel/tree/main/docs/examples for more examples.
```

## Generating traces with custom attributes

Run with `--semconv` and inspect the output. Each span carries attributes drawn from the custom convention definitions.

```bash
motel run --stdout --duration 200ms --semconv /tmp/motel-semconv /tmp/motel-payments.yaml 2>/dev/null | jq -rs "[.[].Attributes[] | select(.Key | startswith(\"payments.\"))] | group_by(.Key) | map(.[0].Key) | sort | .[]"
```

```output
payments.amount_cents
payments.currency
payments.merchant_id
payments.transaction_type
```

All four custom attributes appear on the generated spans: the enum, integer, and string types all produce values drawn from their defined examples.

## Extending the embedded registry

Custom conventions are additive — the full upstream OTel registry is still available. You can mix standard and custom domains in the same topology.

```bash
cat > /tmp/motel-mixed.yaml << 'EOF'
version: 1
services:
  gateway:
    operations:
      checkout:
        duration: 30ms +/- 10ms
        domain: http
        calls:
          - payments.charge
  payments:
    operations:
      charge:
        duration: 20ms +/- 5ms
        domain: payments
traffic:
  rate: 10/s
EOF
motel run --stdout --duration 200ms --semconv /tmp/motel-semconv /tmp/motel-mixed.yaml 2>/dev/null | jq -rs "
  [.[].Attributes[] | select(.Key | startswith(\"http.\") or startswith(\"payments.\"))]
  | group_by(.Key) | map(.[0].Key) | sort | group_by(split(\".\")[0])
  | map(\"\\(.[0] | split(\".\")[0]) domain: \\(length) attributes\")
  | .[]"
```

```output
http domain: 10 attributes
payments domain: 4 attributes
```

The gateway spans carry standard HTTP attributes from the embedded registry, while the payments spans carry the custom attributes we defined. Both registries are available simultaneously.

## Error handling

The `--semconv` flag validates that the path exists and is a directory.

```bash
motel validate --semconv /nonexistent /tmp/motel-payments.yaml 2>&1 | head -1
```

```output
Error: --semconv directory: stat /nonexistent: no such file or directory
```
