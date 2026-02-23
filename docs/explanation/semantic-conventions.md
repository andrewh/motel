# How motel uses OTel semantic conventions

[Semantic conventions](https://github.com/open-telemetry/semantic-conventions)
are an OpenTelemetry standard that defines a common set of attributes for
telemetry data — their names, types, allowed values, and which signal types
they apply to. Semantic conventions are maintained by a dedicated SIG within
the OpenTelemetry project, separate from the SDK and collector SIGs.

[Weaver](https://github.com/open-telemetry/weaver) is a toolkit for
managing telemetry schemas built on semantic conventions. It validates
registries, generates code and documentation from them, and can check
emitted telemetry against a schema at runtime.

motel uses the semantic convention registry data to generate realistic
span attributes automatically.

## What motel takes from the registry

motel vendors the upstream semantic convention YAML files in
`third_party/semconv/model/` (currently v1.39.0, tracked in
`third_party/semconv/VERSION`). These files describe hundreds of
attributes across domains like HTTP, database, messaging, RPC, and others.

The `pkg/semconv` package parses these files into a `Registry` that indexes
groups and attributes by ID and domain. Attribute generators are then
created from the registry definitions:

- **Enum attributes** sample uniformly from the non-deprecated members
- **String, int, double** attributes sample from the definition's example
  values, falling back to a static default when no examples are defined
- **Boolean attributes** produce true/false with equal probability

Deprecated attributes are silently skipped. Template and array attribute
types are not yet supported and are also skipped.

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

To load additional definitions at runtime, pass a directory of registry
YAML files with the `--semconv` flag:

    motel run --semconv ./my-conventions/ topology.yaml

The directory structure mirrors the embedded registry — each subdirectory
becomes a domain. User-provided definitions are merged with the embedded
defaults, so custom domains work alongside the upstream ones.

Alternatively, definitions can be vendored at compile time into
`third_party/semconv/model/` to embed them in the binary.

## How motel parses the registry

motel has its own YAML parser (`pkg/semconv`) that reads the subset of
the registry format it needs: groups, attributes, types, and enum
members. It does not depend on Weaver or any external tooling at build
time or runtime.
