# How motel uses OTel Weaver

[Weaver](https://github.com/open-telemetry/weaver) is an OpenTelemetry tool
for managing semantic conventions. It defines a YAML registry format that
describes attributes, their types, allowed values, and which signal types they
apply to.

motel uses Weaver's registry data to generate realistic span attributes
automatically.

## What motel takes from Weaver

motel vendors the upstream semantic convention YAML files in
`third_party/semconv/model/`. These files use Weaver's registry format and
describe hundreds of attributes across domains like HTTP, database, messaging,
RPC, and others.

The `pkg/semconv` package parses these files into a `Registry` that indexes
groups and attributes by ID and domain. The `pkg/semconv/generate.go` module
then creates attribute generators from the registry definitions:

- **Enum attributes** produce values sampled from the defined members
- **String, int, double** attributes produce realistic placeholder values
- **Boolean attributes** produce true/false with equal probability

## The domain field

When a topology operation specifies a `domain` (e.g. `http`, `db`, `rpc`),
motel looks up the matching semantic convention groups and automatically
generates the standard attributes for that domain. This means a topology
like:

```yaml
services:
  gateway:
    operations:
      GET /users:
        domain: http
        duration: 30ms +/- 10ms
```

produces spans with `http.request.method`, `http.response.status_code`,
`url.scheme`, and other HTTP semantic convention attributes — without
listing them individually.

## Custom definitions

The embedded conventions cover the upstream OTel standard, but
organisations often define their own semantic conventions for internal
services or business-specific attributes.

Currently, custom definitions can be added at compile time by vendoring
additional Weaver registry YAML files into `third_party/semconv/model/`.
They'll be embedded in the binary alongside the upstream definitions.

Runtime customisation — pointing motel at a local directory or a remote
Weaver registry server without recompiling — is tracked in
[issue 28](https://github.com/andrewh/motel/issues/28).

## What motel does not use Weaver for

motel does not use the Weaver CLI tool itself. It only consumes the
registry YAML data that Weaver defines and manages. motel has its own
YAML parser (`pkg/semconv`) that reads the subset of the registry format
it needs (groups, attributes, types, enum members).

motel also does not use Weaver's code generation, validation, or schema
comparison features. The relationship is data-only: Weaver defines the
attribute catalogue, motel reads it.
