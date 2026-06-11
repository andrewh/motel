# AGENTS.md - motel

Synthetic OpenTelemetry generator. This file follows the [agents.md](https://agents.md)
convention and provides guidance for AI coding agents working in this repository.

## Project Overview

- **Module**: `github.com/andrewh/motel`
- **Language**: Go 1.25+, modern constructs (`any`, `context.Context`)
- **Binary**: `build/motel` (always build to the `build/` directory)

## Quick Commands

- **Build**: `make build` (outputs `build/motel`)
- **Test**: `make test` (excludes `third_party/`; replays fuzz corpus automatically)
- **Lint**: `make lint` (`gofmt -s` and `go vet`)

## Repository Structure

```
build/              # Built binaries
cmd/motel/          # CLI entry point and cobra commands
pkg/synth/          # Simulation engine, topology, traffic, scenarios, logs
pkg/synth/traceimport/  # Import pipeline: parse → tree → stats → marshal → validate
pkg/semconv/        # OpenTelemetry semantic convention registry
tools/dgg2motel/    # Converter tool (see tools/dgg2motel/README.md)
third_party/        # Vendored semantic convention YAML data
docs/               # Documentation, examples, demos, man pages
docs/explanation/import-pipeline/  # Worked example of the import inference pipeline
docs/explanation/property-testing.md  # Property testing rationale, patterns, and fuzz workflow
docs/how-to/        # How-to guides (e.g. model-your-services.md)
docs/examples/      # Example topology YAML files
docs/demos/         # Showboat demo scripts
docs/research/      # Related academic work and references
docs/reference/     # CLI reference
docs/tutorials/     # Getting started tutorial
```

## Key Concepts

- **Topology**: YAML file defining services, operations, calls, and traffic. This is the established term — not "config" or "schema"
- **Scenario**: Time-windowed overrides layered on top of a topology. Separate concept from topology
- **Operation.Ref**: Pre-computed `"service.operation"` string set during `BuildTopology`. Use `op.Ref` instead of concatenating `op.Service.Name + "." + op.Name`

## Code Quality

- Wrap errors with `%w`; use sentinel errors
- Use constants for often-used string values to prevent typos
- No magic numbers; use named constants or variables
- No descriptive single-line comments
- Professional, concise tone; no emojis
- Always ensure tests pass before committing

## Commits and Releases

- Single task per commit, no AI attribution in commit messages
- Tag and release only for user-visible features or bug fixes, not for lint/docs/refactoring
- GoReleaser runs via CI on tag push — updates GitHub release and Homebrew tap automatically

## GitHub

- `gh pr edit` hits GraphQL Projects Classic deprecation — use `gh api` REST endpoint instead
- Never use `#N` in PR/issue comments — GitHub auto-links to issue numbers. Use plain numbered lists instead

## Property Testing and Fuzzing

- Property tests use `pgregory.net/rapid` — see `docs/explanation/property-testing.md` for rationale and patterns
- Fuzz targets wrap property tests via `rapid.MakeFuzz` — they live in `fuzz_test.go` files alongside property tests
- Fuzz corpus is stored in `testdata/fuzz/` directories and replays automatically during `make test`
- To run fuzz targets for extended periods: `go test ./pkg/synth/ -fuzz=FuzzName -fuzztime=5m`
- After fuzzing, copy new corpus entries from the Go build fuzz cache (`~/Library/Caches/go-build/fuzz/` on macOS) to `testdata/fuzz/` and commit
