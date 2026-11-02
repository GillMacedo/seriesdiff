# Examples

## `orders/`
A small made-up HTTP service in three versions, each its own Go module using `github.com/prometheus/client_golang` v1.23.2 (Apache-2.0). `before` is the starting point. `after` makes typical instrumentation mistakes: the raw path as the `route` label, new histogram buckets, the `reason` label dropped from `payment_errors_total`, `order_queue_depth` renamed to `orders_queue_depth`, and `cache_entries` changed from a counter to a gauge, plus a new `order_items` histogram. `comments` only adds comments and renames a local variable.

## `openmetrics/`
Prints one real scrape from client_golang with a counter, a histogram, a summary and a gauge, exemplars and `_created` lines. With no argument it writes OpenMetrics; with `text` it writes the Prometheus text format. The timestamps change on every run.

## `monitoring/`
Alert rules and a Grafana-style dashboard written against the `before` metrics, for `--refs`.

## `scrapes/`
All real output, never edited except where noted.

- `orders-before.prom`, `orders-after.prom`, `orders-comments.prom`: from `scripts/scrape-examples.sh` (`make scrapes`), on Go 1.27.3, linux/arm64. Each version gets the same requests: three `POST /orders`, then `GET /orders/1001`, `/orders/1002`, `/orders/1003`, `/orders/abc`, `/pay`, `/pay?fail=card_declined`, `/pay?fail=insufficient_funds` and `/health`.
- `jobs.om` and `jobs.prom`: from `go run ./examples/openmetrics` and `go run ./examples/openmetrics text`.
- `real/node_exporter-*.prom`: official node_exporter release binaries for linux/arm64 (Apache-2.0), checked against the release's `sha256sums.txt`, run with `--web.listen-address=127.0.0.1:19100` and default collectors in a Linux container (kernel 6.12, linuxkit), then scraped with `curl`. `node_exporter-1.7.0-again.prom` is a second scrape of the same 1.7.0 process a second later. The container host name in `node_uname_info` was replaced with `container`, and the `eth0` MAC address with `02:00:00:00:00:01`, in every file.
- `real/prometheus-*.prom`: official Prometheus release binaries for linux/arm64 (Apache-2.0), checked against `sha256sums.txt`, run with an empty scrape configuration and a fresh data directory, then scraped from `127.0.0.1:19090/metrics` about 5 seconds after start. `prometheus-3.0.1-first-scrape.prom` is the first scrape of that process, the others are the third.

## `../tests/testdata/bad/`
Real output from failed commands, used to check that `seriesdiff` refuses them: a failed `go build`, a Go panic, a `curl` connection error, Prometheus's 404 page and query API JSON, node_exporter's HTML landing page, gzip and protobuf scrapes from node_exporter, a `promhttp` 500 response from a collector that failed, empty and blank files, random bytes and prose.
