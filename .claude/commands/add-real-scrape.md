Add a real-release example for: $ARGUMENTS

1. Download two official release binaries of the open-source exporter, check their published checksums, and check its license allows including its metric output.
2. Run each version on a local port with the same flags and the same workload, scrape `/metrics` after a warm-up scrape, and save them as `examples/scrapes/real/<project>-<version>.prom`. Replace host names, MAC addresses and anything else that identifies the machine, the same way in both files.
3. Run `go run ./plugin/scripts compare` on the pair, and check every removed, renamed and lost finding against the project's changelog. Note the versions, platform and license in `examples/README.md`.
4. Add the pair to the tests in `plugin/scripts/seriesdiff_test.go` and `tests/integration_test.go`, then run `make test`.
