# Security

Report vulnerabilities through [GitHub's private vulnerability reporting](https://github.com/GillMacedo/seriesdiff/security/advisories/new), not in a public issue.

`seriesdiff` reads two scrape files and, with `--refs`, the text files under the paths you give it. It never writes files, uses the network or runs other programs. Please report privately if a crafted scrape or reference file could make it do anything else, or hang or use unbounded memory. A wrong or missed finding is a normal bug.
