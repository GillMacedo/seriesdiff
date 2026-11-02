# 0002: Refuse input that may be incomplete

**Decision:** any input that isn't clearly a complete scrape exits 2 and isn't compared. A file that stops where the other one goes on gets a warning, the report, and exit code 2, unless `--trust-ends` is set.

**Why:** earlier tools in this family passed their own tests and still reported a failed build or an unreadable report as all clear. For a metrics diff, a cut-off base makes every later metric look new, which doesn't fail `--fail`, so the bad input would pass CI.

**Cost:** a change that only adds metrics that sort after everything else in the scrape, or only removes the last ones, also triggers the warning. It's rare in the 39 real release pairs tested, where it never happened, and the message says what to check and how to proceed.

**Alternatives:** trusting files that parse. The cut tests in `plugin/scripts/seriesdiff_test.go` showed this let 680 of 8,342 cut inputs through with exit code 0 under `--fail`, before the end check was added.
