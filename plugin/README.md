# seriesdiff plugin

This plugin adds the `comparing-prometheus-metrics` skill to Claude Code. When you ask whether a change breaks Prometheus alerts or dashboards or adds high-cardinality labels, Claude scrapes `/metrics` from the base build and from your change with the same workload, then runs the bundled `scripts/seriesdiff.go` with `go run`. It lists removed or renamed metrics, type changes, removed labels, changed histogram buckets, ID-like label values, lost series and growth, with the rule and dashboard lines that use them. Claude then finds the cause of each one in the code. It needs Go 1.22 or newer. See the repository README for examples and limits.

Install it with `claude plugin marketplace add GillMacedo/seriesdiff` and then `claude plugin install seriesdiff@seriesdiff-marketplace`.
