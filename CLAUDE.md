# seriesdiff

Claude Code skill and Go CLI that compares two Prometheus `/metrics` scrapes, such as one from the base branch and one from a pull request, and reports changes that break alerts and dashboards or grow cardinality.

## Architecture
`plugin/scripts/seriesdiff.go` holds all the logic in one standard-library file, so `go run` needs no setup. `Parse` reads the text format or OpenMetrics into families and refuses input it can't trust, `EndsEarly` spots a file cut off at a line break by comparing it with the other side, `Compare` builds the `Report`, and `BuildRefs` indexes rule and dashboard files for `--refs`. `plugin/skills/comparing-prometheus-metrics/SKILL.md` tells Claude how to get two comparable scrapes and explain the report. See `docs/architecture.md`.

## Commands
- `make test`: gofmt, vet, unit tests, and integration tests in `tests/`, which also run every `$ ` line in the README and check its output
- `make lint`: plugin and marketplace validation
- `make demo`: compare the example scrapes
- `make scrapes`: rebuild `examples/scrapes/orders-*.prom` from `examples/orders` (needs the network the first time)

## Conventions
- Standard library only. Never use the network, write files or run programs from the tool.
- Input that isn't a complete, valid scrape exits 2 with a message. Never report "No changes" or exit 0 for it. Add every new kind of bad input to `tests/testdata/bad/` or to the parser tests.
- Every new report section needs a real scrape in `examples/scrapes/` that it fires on, and must stay quiet on `orders-comments.prom` and `real/node_exporter-1.7.0-again.prom`.
- Scrapes in `examples/scrapes/` are real output. `examples/README.md` says what made each one.
- README examples are generated from real output. If output changes, update the README, or `TestReadmeExamples` fails.

## Do and don't
- Do say a finding comes from two scrapes and depends on the workload.
- Don't say metrics are unchanged because the report is empty.

## Definition of done
`make test` and `make lint` pass, and `ROADMAP.md`, the README and docs are updated when relevant.
