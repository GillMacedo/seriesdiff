# Roadmap

## Next
- A Markdown output for pull request comments
- Read Prometheus rule files and Grafana dashboard JSON properly, so `--refs` can tell a query from a comment and follow `__name__` matchers
- Show which recording rules depend on a changed metric, and which alerts depend on those rules

## Later
- A `scrape` command that runs a command, waits for a port, and saves the scrape, so a whole comparison is one line in CI
- Test scrapes from the Python, Java and Node clients and the OpenTelemetry Prometheus exporter, and add them to `examples/scrapes/`
- Let a project list label values that are expected to look like IDs, such as known tenant names
