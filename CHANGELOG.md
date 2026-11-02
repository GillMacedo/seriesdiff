# Changelog

All notable changes are documented here, in [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format. This project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-11-02
### Added
- `compare` command: compares two saved `/metrics` scrapes in the Prometheus text format or OpenMetrics
- Reports removed metrics with a likely new name, type changes, removed labels, changed histogram buckets, label values that look like IDs, lost series and label values, and series growth, plus new metrics and labels
- `--refs` finds the lines in rule files, dashboards and other text files that use each changed metric, label or lost value
- Refuses empty, binary, garbled and cut-off scrapes with exit code 2, and warns when one file stops where the other goes on
- Normalizes OpenMetrics counter names, `le` and `quantile` values and `_created` lines, so text and OpenMetrics scrapes of the same service compare as equal
- `--fail`, `--max-growth`, `--json` and `--trust-ends` options
- Claude Code plugin with the `comparing-prometheus-metrics` skill
