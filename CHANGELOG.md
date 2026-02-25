# Changelog

All notable changes to motel are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.6.4] - 2026-02-25

### Added

- Time offset (`--offset`) for events from `motel run` (#98)
- DeathStarBench example topologies (#97)

### Fixed

- Integer attribute values for numeric weighted choices (#96)

## [0.6.3] - 2026-02-24

### Fixed

- Per-service `service.name` resource applied correctly to metrics and logs (#91)
- Concurrent shutdown of exporters extracted into helper (#91)

## [0.6.2] - 2026-02-24

### Added

- Accept HTTP/HTTPS URLs as topology source with size limits and redirect caps (#89)
- How-to guide for visualising traces

## [0.6.1] - 2026-02-24

### Added

- Percentile distribution reporting (p50/p95/p99/max) to `motel check` (#84)
- Godoc comments on exported symbols (#83)

## [0.6.0] - 2026-02-23

### Added

- `motel check` command for structural topology analysis (#67)
- Internal gateway example topology
- Fuzz targets for property tests

## [0.5.4] - 2026-02-23

### Fixed

- Manpage missing from release archives (#65)
- Inaccurate claims about Weaver and semantic convention format ownership
- Overlapping scenario labels in SVG preview

## [0.5.3] - 2026-02-23

### Changed

- Manpage included in Homebrew install (#65)

## [0.5.2] - 2026-02-23

### Added

- `--pprof` flag on `motel run` for profiling (#64)
- `motel preview` command for traffic visualisation
- Engine benchmarks and performance profile documentation
- How-to guides for collector testing, observability testing, and otel-cli (#60)
- How-to guide for testing backend integrations (#59)

### Removed

- Poisson traffic pattern

## [0.5.1] - 2026-02-21

### Added

- `--label-scenarios` flag for scenario provenance on spans (#46)
- Property tests using `pgregory.net/rapid` (#38)

### Fixed

- LICENSE updated to exact Apache 2.0 text (#47)

## [0.5.0] - 2026-02-21

### Added

- `--semconv` flag for user-provided semantic conventions (#37)

### Fixed

- Lint warnings: unchecked `fmt.Fprintf` return, gofmt alignment (#34)

## [0.4.0] - 2026-02-21

### Changed

- Improved CLI error messages and usability (#33)

## [0.3.1] - 2026-02-21

### Changed

- Better validation errors and missing collector detection (#31)

## [0.2.1] - 2026-02-21

### Added

- `gofmt -s` check to lint target (#20)

### Changed

- Pre-compute `Operation.Ref` to avoid hot-path string concatenation (#23)
- Restored and reorganised documentation (#22)

### Dependencies

- OTel exporters bumped to latest (#21)
- `actions/setup-go` 5 → 6 (#14)
- `actions/checkout` 4 → 6, `actions/upload-artifact` 4 → 6 (#12, #13)
- `github.com/spf13/cobra` 1.9.1 → 1.10.2 (#16)

## [0.2.0] - 2026-02-20

Initial public release. Extracted from motel-kitchen (#1).

### Added

- `motel run` command for generating synthetic OpenTelemetry traces
- `motel.version` resource attribute (#10)
- YAML topology format for defining services, operations, calls, and traffic
- OTLP and stdout exporters
- Homebrew tap via GoReleaser
- Community files (CONTRIBUTING, CODE_OF_CONDUCT, SECURITY)

[0.6.4]: https://github.com/andrewh/motel/compare/v0.6.3...v0.6.4
[0.6.3]: https://github.com/andrewh/motel/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/andrewh/motel/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/andrewh/motel/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/andrewh/motel/compare/v0.5.4...v0.6.0
[0.5.4]: https://github.com/andrewh/motel/compare/v0.5.3...v0.5.4
[0.5.3]: https://github.com/andrewh/motel/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/andrewh/motel/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/andrewh/motel/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/andrewh/motel/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/andrewh/motel/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/andrewh/motel/compare/v0.2.1...v0.3.1
[0.2.1]: https://github.com/andrewh/motel/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/andrewh/motel/releases/tag/v0.2.0
