# CLAUDE.md - motel

Synthetic telemetry generator for OpenTelemetry.

## Core Standards

- **Module**: `github.com/andrewh/motel`
- **Binary**: `build/motel` (always build to `build/` directory)
- **Go**: Modern constructs (`any`, `context.Context`)
- **Error Handling**: Wrap errors with `%w`, use sentinel errors
- **Tone**: Professional, concise, no emojis
- **Commits**: Single task per commit, no Claude attribution

## Quick Commands

- **Build**: `make build`
- **Test**: `make test`
- **Lint**: `make lint`

## File Paths & Structure

```
build/          # Built binaries
cmd/motel/      # CLI entry point and cobra commands
pkg/synth/      # Simulation engine, topology, traffic, scenarios
pkg/semconv/    # OpenTelemetry semantic convention registry
third_party/    # Vendored semantic convention YAML data
docs/           # Documentation, examples, demos, man pages
```

## Code Quality

- Use constants for often-used string values to prevent typos
- No magic numbers; use named constants or variables
- No descriptive single-line comments
- Always ensure tests pass before committing
