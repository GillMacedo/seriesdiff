# 0001: One standard-library Go file in a skill

**Decision:** the tool is a single Go file with no dependencies, run with `go run`, bundled in a Claude Code skill.

**Why:** an agent can already build a service, run it and save a scrape with `curl`. What it lacks is a reliable way to compare two large scrapes, which is a pure function of two files. A script does that, and `go run` is the whole install. An MCP server would add a binary and an npm package without giving the agent anything new. No dependency on `prometheus/common` keeps it that way; its parser would add several modules to vendor.

**Alternatives:** an MCP server with the official Go SDK, like `stuckgo`. It would make sense if agents needed this without a shell, which isn't the case here, because getting the scrapes needs a shell anyway.
