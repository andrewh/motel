# CLAUDE.md - motel

Synthetic OpenTelemetry generator.

## Model

- Use Claude Opus (latest available) for all work on this project

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
build/              # Built binaries
cmd/motel/          # CLI entry point and cobra commands
pkg/synth/          # Simulation engine, topology, traffic, scenarios
pkg/synth/traceimport/  # Import pipeline: parse → tree → stats → marshal → validate
pkg/semconv/        # OpenTelemetry semantic convention registry
third_party/        # Vendored semantic convention YAML data
docs/               # Documentation, examples, demos, man pages
docs/explanation/import-pipeline/  # Worked example of the import inference pipeline
docs/explanation/property-testing.md  # Property testing rationale, patterns, and fuzz workflow
docs/how-to/        # How-to guides (e.g. model-your-services.md)
docs/examples/      # Example topology YAML files
docs/demos/         # Showboat demo scripts
docs/reference/     # CLI reference
docs/tutorials/     # Getting started tutorial
```

## Key Concepts

- **Topology**: YAML file defining services, operations, calls, and traffic. This is the established term — not "config" or "schema"
- **Scenario**: Time-windowed overrides layered on top of a topology. Separate concept from topology
- **Operation.Ref**: Pre-computed `"service.operation"` string set during `BuildTopology`. Use `op.Ref` instead of concatenating `op.Service.Name + "." + op.Name`

## Releases

- Tag and release only for user-visible features or bug fixes, not for lint/docs/refactoring
- GoReleaser runs via CI on tag push — updates GitHub release and Homebrew tap automatically

## GitHub

- `gh pr edit` hits GraphQL Projects Classic deprecation — use `gh api` REST endpoint instead
- Never use `#N` in PR/issue comments — GitHub auto-links to issue numbers. Use plain numbered lists instead

## Code Quality

- Use constants for often-used string values to prevent typos
- No magic numbers; use named constants or variables
- No descriptive single-line comments
- Always ensure tests pass before committing

## Property Testing and Fuzzing

- Property tests use `pgregory.net/rapid` — see `docs/explanation/property-testing.md` for rationale and patterns
- Fuzz targets wrap property tests via `rapid.MakeFuzz` — they live in `fuzz_test.go` files alongside property tests
- Fuzz corpus is stored in `testdata/fuzz/` directories and replays automatically during `make test`
- To run fuzz targets for extended periods: `go test ./pkg/synth/ -fuzz=FuzzName -fuzztime=5m`
- After fuzzing, copy new corpus entries from `~/Library/Caches/go-build/fuzz/` (macOS) to `testdata/fuzz/` and commit
