# 0003: Compare scrapes, not source code

**Decision:** the inputs are two `/metrics` scrapes, not the source diff.

**Why:** metric names, labels and buckets are often built at run time, in helpers, or by libraries and exporters. Reading the scrape sees exactly what Prometheus will see, from any language. It also works for exporter upgrades, where there's no source diff to read.

**Cost:** both scrapes need the same workload, and code paths the workload doesn't run aren't covered. The README and the skill both say so, and the skill asks Claude to name the parts of the service the workload missed.
