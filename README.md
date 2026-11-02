# seriesdiff: Prometheus metrics diff for pull requests

[![CI](https://github.com/GillMacedo/seriesdiff/actions/workflows/ci.yml/badge.svg)](https://github.com/GillMacedo/seriesdiff/actions/workflows/ci.yml) [![MIT license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**A Claude Code skill and Go CLI for backend developers and SREs who change instrumented code and want to know, before merging, whether the change breaks Prometheus alerts and Grafana dashboards or blows up metric cardinality. It compares two `/metrics` scrapes, one from the base branch and one from your change, and lists removed or renamed metrics, type changes, removed labels, changed histogram buckets, label values that look like IDs, lost series and series growth. With `--refs` it names the alert rules and dashboard lines that use each changed metric.**

Renaming a metric, dropping a label or switching a route label from the template to the raw path usually pass code review and CI, because tests rarely check metric names and labels. The first sign is an alert that silently stops firing or a Prometheus server running out of memory. `seriesdiff` reads the scrape files, so it works with any exporter or client library, and it refuses an empty, garbled or cut-off scrape instead of calling it clean.

## What you can ask Claude

- "Does my branch rename or remove any Prometheus metrics our alerts use?"
- "Will this change add high-cardinality labels to our metrics?"
- "Compare /metrics from main and from my branch and tell me what breaks."
- "We upgraded node_exporter. Which of our dashboards will lose data?"

## Install

As a Claude Code plugin:

    claude plugin marketplace add GillMacedo/seriesdiff
    claude plugin install seriesdiff@seriesdiff-marketplace

Or as a CLI with Go 1.22 or newer. It has no dependencies:

    go run github.com/GillMacedo/seriesdiff/plugin/scripts@latest compare base.prom head.prom

## Example

`examples/orders` holds three versions of a small made-up HTTP service instrumented with the real `client_golang` library. `after` makes typical pull request mistakes: it labels requests with the raw path instead of the route template, changes the latency buckets, drops the `reason` label from payment errors, renames a gauge, and turns a counter into a gauge. `scripts/scrape-examples.sh` builds each version, sends the same requests, and saves `/metrics`. `examples/monitoring` holds alert rules and a dashboard written against the `before` metrics. From a clone of this repo:

    $ go run ./plugin/scripts compare --refs examples/monitoring examples/scrapes/orders-before.prom examples/scrapes/orders-after.prom
    1 removed metric, 1 type change, 1 removed label, 1 bucket change, 2 labels with ID-like values, 2 metrics that lost series, 2 new metrics
    Series: 114 -> 114. Metrics: 45 -> 46.

    Removed metrics (queries, alerts and dashboards using them get no data):
      order_queue_depth  gauge, 1 series
          probably renamed to orders_queue_depth
          used in examples/monitoring/alerts.yml:11, examples/monitoring/dashboards/orders.json:22

    Type changed (rate() and histogram_quantile() depend on the type):
      cache_entries  counter -> gauge
          used in examples/monitoring/dashboards/orders.json:17

    Removed labels (by() and label matchers using them stop matching):
      payment_errors_total  label reason, which had 2 values
          used in examples/monitoring/alerts.yml:14, examples/monitoring/dashboards/orders.json:12

    Histogram buckets changed (quantiles across the change and le= matchers are affected):
      http_request_duration_seconds  removed le 0.005, 0.025, 0.05, 0.25, 0.5, 2.5, 5, 10; added le 0.001
          used in examples/monitoring/alerts.yml:6, examples/monitoring/alerts.yml:8

    Label values that look like IDs (one series per ID, which grows without limit):
      http_request_duration_seconds  label route: "/orders/1001", "/orders/1002", "/orders/1003" (7 values)
      http_requests_total  label route: "/orders/1001", "/orders/1002", "/orders/1003" (7 values)

    Lost series (alerts and dashboards that select them get no data):
      http_request_duration_seconds  56 -> 49 series, label route 4 -> 7 values
          route no longer has "/orders/{id}"
          used in examples/monitoring/alerts.yml:8
      http_requests_total  6 -> 8 series, label route 4 -> 7 values
          route no longer has "/orders/{id}"

    New metrics:
      order_items  histogram, 6 series
      orders_queue_depth  gauge, 1 series

    Searched 2 files for references.

The `comments` version only adds comments and renames a local variable, and stays quiet:

    $ go run ./plugin/scripts compare examples/scrapes/orders-before.prom examples/scrapes/orders-comments.prom
    No changes in metric names, types, labels, buckets or series counts (45 metrics, 114 series).

### Real releases

These scrapes come from the official release binaries of node_exporter and Prometheus, run in the same Linux container.

node_exporter 1.10.1 shipped a typo in a metric name, fixed in 1.10.2 ("meminfo: Fix typo in Zswap metric name"). Anything that queried the 1.10.1 name gets no data after the upgrade:

    $ go run ./plugin/scripts compare examples/scrapes/real/node_exporter-1.10.1.prom examples/scrapes/real/node_exporter-1.10.2.prom
    1 removed metric, 1 new metric
    Series: 1404 -> 1404. Metrics: 310 -> 310.

    Removed metrics (queries, alerts and dashboards using them get no data):
      node_memory_Zswap_ytes  gauge, 1 series
          probably renamed to node_memory_Zswap_bytes

    New metrics:
      node_memory_Zswap_bytes  gauge, 1 series

node_exporter 1.9.0 moved the Linux meminfo collector to the procfs library, and client_golang removed `go_memstats_lookups_total`, which was always 0, according to the two changelogs. Seven `node_memory_*` metrics that this container's kernel reports disappeared. 1.10.1 added `Zswap` back, under the misspelled name shown above:

    $ go run ./plugin/scripts compare examples/scrapes/real/node_exporter-1.8.2.prom examples/scrapes/real/node_exporter-1.9.1.prom
    8 removed metrics, 8 new metrics, 1 new label
    Series: 1397 -> 1403. Metrics: 309 -> 309.

    Removed metrics (queries, alerts and dashboards using them get no data):
      go_memstats_lookups_total  counter, 1 series
      node_memory_FileHugePages_bytes  gauge, 1 series
      node_memory_FilePmdMapped_bytes  gauge, 1 series
      node_memory_Hugetlb_bytes  gauge, 1 series
      node_memory_KReclaimable_bytes  gauge, 1 series
      node_memory_SecPageTables_bytes  gauge, 1 series
      node_memory_Zswap_bytes  gauge, 1 series
      node_memory_Zswapped_bytes  gauge, 1 series

    New metrics:
      go_gc_gogc_percent  gauge, 1 series
      go_gc_gomemlimit_bytes  gauge, 1 series
      go_sched_gomaxprocs_threads  gauge, 1 series
      node_filesystem_mount_info  gauge, 4 series
      node_filesystem_purgeable_bytes  gauge, 4 series
      node_netstat_TcpExt_TCPRcvQDrop  untyped, 1 series
      process_network_receive_bytes_total  counter, 1 series
      process_network_transmit_bytes_total  counter, 1 series

    New labels on existing metrics:
      node_disk_info  label rotational, 1 value

Prometheus 3.0.0 replaced the old web UI and added UI notifications, according to its changelog. In the same release, two `handler` label values are gone from its own request counter, and three notification metrics are new:

    $ go run ./plugin/scripts compare examples/scrapes/real/prometheus-2.55.1.prom examples/scrapes/real/prometheus-3.0.1.prom
    1 metric that lost series, 3 new metrics
    Series: 496 -> 504. Metrics: 203 -> 206.

    Lost series (alerts and dashboards that select them get no data):
      prometheus_http_requests_total  51 -> 56 series, label handler 51 -> 56 values
          handler no longer has "/starting", "/static/*filepath"

    New metrics:
      prometheus_api_notification_active_subscribers  gauge, 1 series
      prometheus_api_notification_updates_dropped_total  counter, 1 series
      prometheus_api_notification_updates_sent_total  counter, 1 series

The same node_exporter version scraped twice stays quiet. Only values changed:

    $ go run ./plugin/scripts compare examples/scrapes/real/node_exporter-1.7.0.prom examples/scrapes/real/node_exporter-1.7.0-again.prom
    No changes in metric names, types, labels, buckets or series counts (308 metrics, 1393 series).

### Input it won't trust

A scrape that failed, such as an error page saved by `curl`, is refused with exit code 2:

    $ go run ./plugin/scripts compare examples/scrapes/orders-before.prom tests/testdata/bad/http-404.txt 2>&1 | head -n 2
    seriesdiff: head is not a usable scrape, so nothing was compared.
      tests/testdata/bad/http-404.txt: line 1: no metric name in "404 page not found". This doesn't look like a Prometheus scrape

A scrape cut off in the middle of a histogram is refused too. One cut off between two metrics still parses, so `seriesdiff` compares where each file ends. Here head stops where base goes on, which is how a cut-off file looks, so the report comes after a warning and the exit code is 2:

    $ head -n 196 examples/scrapes/orders-before.prom | go run ./plugin/scripts compare examples/scrapes/orders-before.prom - 2>&1 | head -n 8
    WARNING: head may be cut off, so this comparison can't be trusted. standard input ends
      with process_virtual_memory_max_bytes. examples/scrapes/orders-before.prom has that
      series too, and then 4 more that head doesn't have at all, which is what a scrape cut
      off at a line break looks like. If the change really removes only series that sort after
      it, check that head is complete and run again with --trust-ends.

    2 removed metrics
    Series: 114 -> 110. Metrics: 45 -> 43.

## Usage

    seriesdiff compare [--refs PATH]... [--max-growth N] [--fail] [--json] [--trust-ends] BASE HEAD

- `BASE` and `HEAD` are saved `/metrics` output in the Prometheus text format or OpenMetrics, for example from `curl -sf http://localhost:8080/metrics -o head.prom`. Use `-` for standard input. A text scrape and an OpenMetrics scrape of the same service compare as equal.
- `--refs PATH` searches files or directories, such as Prometheus rule files and Grafana dashboard JSON, for lines that name a changed metric. Repeat it for more paths. `.git`, `vendor` and `node_modules` are skipped.
- `--fail` exits 1 when there are removed metrics, type changes, removed labels, bucket changes, ID-like label values, lost series, or a metric whose series grew by more than `--max-growth` percent (default 50). New metrics and new labels don't fail.
- `--json` prints the full result.
- `--trust-ends` turns off the cut-off check described above, for a change that really only adds or removes series at the end of the scrape.
- Exit code 2 means bad usage, an input that isn't a complete scrape, or one that may be cut off.

### Getting two comparable scrapes

Series only appear after the code that records them runs, so both scrapes need the same workload. Prometheus itself shows why: two of its HTTP histograms only appear after it has served a request, so its first scrape differs from a later one:

    $ go run ./plugin/scripts compare examples/scrapes/real/prometheus-3.0.1-first-scrape.prom examples/scrapes/real/prometheus-3.0.1.prom
    2 new metrics
    Series: 481 -> 504. Metrics: 204 -> 206.

    New metrics:
      prometheus_http_request_duration_seconds  histogram, 12 series
      prometheus_http_response_size_bytes  histogram, 11 series

A reliable way is to run your service's integration or smoke tests against each build and scrape at the end. `scripts/scrape-examples.sh` shows the pattern with `curl`.

### In CI

    - run: go run github.com/GillMacedo/seriesdiff/plugin/scripts@v0.1.0 compare --fail --refs monitoring/ base.prom head.prom

You need a step that builds and runs the base and the head and saves both scrapes first. This hasn't been run in GitHub Actions yet.

## What it reports

| Section | Meaning | Counts for `--fail` |
|---|---|---|
| Removed metrics | A family in base is missing in head. A likely new name is shown when an added family has the same labels and a close name | yes |
| Type changed | `# TYPE` differs, such as counter to gauge | yes |
| Removed labels | A label that base used on a family is on none of its series in head | yes |
| Histogram buckets changed | The set of `le` boundaries differs | yes |
| Label values that look like IDs | Numbers of 5 or more digits, UUIDs, long hex strings, IP addresses, emails, or paths with such a segment, in a label that had none in base | yes |
| Lost series | A family has fewer series, or a label lost values, such as `code="500"` | yes |
| Over N% more series | A family's series count grew by more than `--max-growth` | yes |
| A few more series | Smaller growth, with the label that grew most | no |
| New metrics, new labels | Added families and labels | no |

A series is one sample line, which is how Prometheus stores it, so each histogram bucket counts. `_created` lines from OpenMetrics aren't counted. Metrics whose name ends in `_info`, such as `go_info`, aren't reported when only a label value changes, because their versions change with every build.

## Limits

- **Two scrapes, not your code.** It can't see a metric that the workload didn't trigger, in either build.
- **References are text matches.** `--refs` finds lines where the metric name appears as a whole word. It doesn't parse PromQL, so a name built with `__name__=~"..."` or a Grafana variable isn't found, and a line that names a metric in a comment is.
- **Cut-off detection has limits.** It catches a scrape cut inside a line, after a `# HELP` or `# TYPE` line, inside a histogram or summary, without the OpenMetrics `# EOF`, or at a line break where one file stops and the other goes on. It can't tell a cut-off file from one where the change removed or added only the last metrics in the file, so those runs need `--trust-ends`.
- **A partial scrape that looks complete isn't caught.** A client that skips a failing collector and serves the rest, such as `promhttp` with `ContinueOnError`, produces a valid file. Check the scrape command's exit code and the service's logs.
- **ID detection is a pattern match.** It can miss IDs in other shapes and can flag a value that only looks like one. It doesn't know how many values a label will get in production.
- **Text formats only.** Protobuf and compressed scrapes are refused. Native histogram buckets aren't in the text format, so their bucket changes aren't seen.
- `seriesdiff` only reads files. It doesn't use the network or run anything. Claude sends what it reads to its model.

## FAQ

**How do I find out if my PR breaks Prometheus alerts?** Scrape `/metrics` from the base build and from your branch with the same workload, then run `seriesdiff compare --refs path/to/rules base.prom head.prom`. Removed metrics, removed labels and lost label values are listed with the rule lines that use them.

**How do I catch high-cardinality labels before they reach production?** The ID-like values section flags labels such as a raw URL path or a user ID. `--max-growth` catches a metric whose series count jumps.

**Does it work with OpenTelemetry, Python, Java or Node?** It reads any scrape in the Prometheus text format or OpenMetrics, whatever produced it. It has been tested only on output from Go programs: client_golang, node_exporter and Prometheus.

**How is this different from `promtool check metrics`?** `promtool` lints one scrape for naming problems. `seriesdiff` compares two scrapes and reports what changed between them.

**How is this different from pint?** pint lints Prometheus alerting and recording rules, and most of its checks query the Prometheus server the rules run on. `seriesdiff` starts from the metrics instead: it compares what two builds of your service export, before deploy, without a server.

**Is it an MCP server?** No. It's a skill with one Go source file, so `go run` is all it needs.

## Tested on

- **The example service**, built from `examples/orders` with client_golang v1.23.2 on Go 1.27.3 (linux/arm64), scraped by `scripts/scrape-examples.sh`. The integration tests in `tests/` also build it, run it, send the requests and scrape it over HTTP, then compare live, when client_golang is in the module cache.
- **Real releases** of node_exporter (1.7.0, 1.8.2, 1.9.1, 1.10.1 and 1.10.2) and Prometheus (2.55.1 and 3.0.1), official linux/arm64 binaries with checked SHA-256 sums, run in a Linux container. The removed and renamed metrics shown above match entries in the projects' changelogs. 39 pairs of these scrapes were compared, and none raised a cut-off warning.
- **OpenMetrics** from client_golang with exemplars and `_created` lines, made by `examples/openmetrics`, compared against the same registry in the text format.
- **Bad input** on each side, with and without `--fail`: empty and blank files, random bytes, prose, a failed `go build`, a Go panic, a `curl` connection error, a 404 page, an HTML page, Prometheus API JSON, a gzip scrape, a protobuf scrape and a `promhttp` error response. All are refused with exit code 2.
- **Go 1.22.12** runs the unit and integration tests in short mode, which shortens the cut-off sweep below.
- **Cut-off scrapes:** the unit tests cut six pairs of real scrapes at every line break and at 50 points inside lines, on each side, with and without `--fail`. All of them exit 2 and say the input can't be trusted.

It hasn't been tested in a live Claude Code session, on Windows or macOS, in GitHub Actions, through `go run ...@latest` (which needs the published repo), or on scrapes from Python, Java, Node or OpenTelemetry exporters.

## Contributing

Contributions are welcome, from people and from people working with AI agents. See [CONTRIBUTING.md](CONTRIBUTING.md). Good first issues are [listed here](https://github.com/GillMacedo/seriesdiff/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

### How pull requests work

1. **Open a PR.** Fork the repo, work on a branch, and keep it to one change. Write a title that says what changes for users, because it becomes the changelog line: `feat:` goes under Added and `fix:` under Fixed.
2. **Automatic check.** Code changes need tests, and `make test` must pass. Changes over 30 lines need a linked open issue (`Closes #<number>`), and a PR can have at most 800 changed lines. If something is missing, a comment tells you what to fix.
3. **Review.** Claude reviews each PR automatically. Code PRs also get a security review and are tested in a sandbox. The review then merges the PR or asks for changes. PRs that change CI, agent settings, npm or Python dependencies, vendored code or build files can't be merged this way, and a comment says so. The same goes for anything the security review doesn't clear.
4. **Release.** A merge that changes what users install becomes a new version within about two hours, with a GitHub Release and, if the project has one, its npm package. Docs and CI changes wait for the next release.

## AI disclosure

This project was largely generated with Claude Code and reviewed by its maintainer.

## About me

I'm Giulliano Macedo, and I build developer tools at GoTech Solutions. Follow along or get in touch:

- GitHub: [GillMacedo](https://github.com/GillMacedo)
- LinkedIn: [giulliano](https://www.linkedin.com/in/giulliano/)
- X: [@giumacedo](https://x.com/giumacedo)

If `seriesdiff` saved you some prompts, a star helps others find it.

---

Built by [Giulliano Macedo](https://github.com/GillMacedo), GoTech Solutions · [LinkedIn](https://www.linkedin.com/in/giulliano/) · [X](https://x.com/giumacedo)
