---
name: comparing-prometheus-metrics
description: Compares two Prometheus /metrics scrapes, such as one from the base branch and one from the current change, and reports removed or renamed metrics, type changes, removed labels, changed histogram buckets, ID-like label values, lost series and cardinality growth, with the alert rules and dashboards that use them. Use when the user asks whether a change breaks Prometheus alerts or Grafana dashboards, renames or removes metrics, adds high-cardinality labels, wants instrumentation reviewed in a pull request, upgrades an exporter, or shares two /metrics outputs.
---

# Comparing Prometheus metrics

Metric changes pass code review and CI, then show up as alerts that never fire or a Prometheus server that runs out of memory. The bundled tool compares two saved scrapes and lists only what changed. It needs Go 1.22 or newer. It reads files and never uses the network.

## Get two comparable scrapes

1. Find how the service exposes metrics: look for `promhttp.Handler`, `prometheus_client`, `micrometer`, `prom-client` or an OpenTelemetry Prometheus exporter, and the port and path, usually `/metrics`.
2. Series appear only after the code that records them runs, so both scrapes need the same workload. Prefer the project's integration or smoke tests. Otherwise send the same fixed list of requests to each build.
3. Build and run the base (for example from `git worktree add /tmp/base "$(git merge-base origin/main HEAD)"`), run the workload, then save the scrape:

       curl -sf http://localhost:PORT/metrics -o /tmp/seriesdiff/base.prom

   Do the same for the working tree into `/tmp/seriesdiff/head.prom`. Check that `curl` exited 0 and stop the service each time. Ask before starting anything that needs credentials, real databases or external services.
4. If the user already has two scrapes, use them.

## Run the tool

    go run "${CLAUDE_PLUGIN_ROOT}/scripts/seriesdiff.go" compare --refs PATH /tmp/seriesdiff/base.prom /tmp/seriesdiff/head.prom

- Pass `--refs` for each directory with Prometheus rules, recording rules or Grafana dashboards in the repo, such as `deploy/`, `monitoring/` or `alerts/`. Find them with a search for `expr:` or `"targets"`.
- Exit code 2 means an input isn't a complete scrape or may be cut off. Report the message and get a new scrape. Never present the results of a run that ended with exit code 2 as a clean result. Use `--trust-ends` only after checking both files end with the same final metric as a fresh scrape.
- `--json` gives the full result if you need to process it.

## Explain each finding

For each item in the report, open the code that registers or records the metric and say why it changed:

1. **Removed metrics and type changes:** find the rename or the new constructor in the diff. If "probably renamed to" is shown, confirm it. List each rule or dashboard line from "used in" that will stop working.
2. **Removed labels and lost series:** say which `by (...)` clauses, matchers and alerts depend on them. A lost value such as `code="500"` can mean an error path is no longer counted.
3. **ID-like label values:** find where the label value comes from, such as `r.URL.Path` instead of the route template, and say how many values it will get in production.
4. **Bucket changes:** say which `histogram_quantile` results and `le=` matchers change, and that quantiles across the deploy aren't comparable.
5. **Growth:** name the label that grew and whether it's bounded.

## Report

1. Lead with what breaks existing alerts and dashboards, with `file:line` for each user.
2. Then cardinality risks, with the label and its source in the code.
3. Propose the smallest fix in the project's style, such as keeping the old name, using the route template, or adding a recording rule for the new name. If a rename is intended, list the rules and dashboards to update in the same pull request.
4. Say which parts of the service the workload didn't exercise, because their metrics can't appear in either scrape.

## Don't

- Don't edit code, rules or dashboards unless the user asks.
- Don't treat "No changes" as proof that metrics are unchanged. Code paths the workload didn't run aren't covered.
- Don't scrape production or shared environments to make a comparison. Use local builds.
- Don't paste full scrapes into the conversation when the report is enough. They can be large.
