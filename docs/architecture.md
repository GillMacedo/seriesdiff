# Architecture

`seriesdiff` is one standard-library Go file, `plugin/scripts/seriesdiff.go`, plus a skill that tells Claude how to use it.

## Flow

1. **Read.** `run` reads both files, or one from standard input.
2. **Parse and check.** `Parse` reads each line as a comment, `# HELP`, `# TYPE`, `# UNIT`, `# EOF` or a sample. It groups samples into families by the most recent `# TYPE` and the suffixes that type allows (`_total`, `_bucket`, `_sum`, `_count` and so on). It refuses the file with an `InputError` when it is empty, binary or not UTF-8, doesn't end with a newline, has a line that isn't valid, repeats a series or a `# TYPE`, ends after metadata with no samples, has a histogram without its `+Inf` bucket or `_count`, has a summary without `_count`, or is OpenMetrics without `# EOF`.
3. **Normalize.** OpenMetrics counter families are renamed to their `_total` sample name, `le` and `quantile` values are written the same way (`1.0` becomes `1`), and `_created` samples aren't counted. A text and an OpenMetrics scrape of the same registry then compare as equal.
4. **Check the ends.** `EndsEarly` finds the last series of one file that the other file also has. If the other file has more series after that point and the first file has none of them, the first file may have been cut off at a line break. The report is printed after a warning, and the exit code is 2, unless `--trust-ends` is set.
5. **Compare.** `Compare` matches families by name and reports removed and added families, type changes, removed and added labels, `le` sets, label values that look like IDs, lost label values, and series counts. A removed family gets a likely new name when an added family has the same label names and a close name.
6. **References.** With `--refs`, `BuildRefs` reads every text file under the given paths and indexes the identifiers on each line. A changed metric is matched against all the sample names a query could use for it, and a lost value also needs the label name and the value in quotes on the same line.
7. **Output.** `WriteText` or JSON. `Report.Problems` decides the `--fail` exit code.

## Why the checks are strict

A tool that compares two files can report "no changes" for the wrong reason: one side is an error page, an empty file, or a scrape cut off by a timeout. Everything in step 2 and step 4 exists so that a bad input never produces a clean result or exit code 0. The unit tests cut real scrapes at every line break to keep it that way.

## What it doesn't do

It doesn't scrape, run services or parse PromQL. The skill does the scraping with `curl`, so the tool stays a pure function of its input files.
