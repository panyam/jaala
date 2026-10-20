# Security policy

## Reporting a vulnerability

Report a vulnerability privately through [GitHub private vulnerability reporting](https://github.com/panyam/jaala/security/advisories/new)
on this repository. If that is unavailable, email sri.panyam@gmail.com with "jaala security" in the
subject. Please do not open a public issue for a suspected vulnerability.

We acknowledge a report within 2 business days. A critical issue (CVSS 7.0 or higher) is fixed or
mitigated within 7 days. The fix ships as a patch release on the current release line, and the
advisory is published once that release is available.

## Supported versions

The latest tagged release receives security fixes. jaala is pre-1.0 and releases patch versions on
v0.1.x, so a fix lands in the next patch.

## Scope

Every module jaala ships, each tagged at the same commit as the root: the engine
(`github.com/panyam/jaala`: `ns`, `stdlib`, `datalog`), `frontends/nl`, `frontends/nlmodels`,
`frontends/mcp`, `frontends/cypher` and `stores/bolt`. The documentation site (`docsite/`) is not
shipped and is out of scope.

jaala is a library: it runs the queries a host hands it, over the facts a host gives it, and every
evaluation runs under the budget and context the host sets. A query text that makes the engine
panic, hang past its budget, or read or write outside the host's Source is in scope. `frontends/mcp`
serves an MCP server over a host's vocabulary, `frontends/nlmodels` calls local model servers, and
`stores/bolt` connects to a Neo4j or Memgraph server, each where the host points it.

## What runs

`make audit` runs every scanner: staticcheck (`make lint`), gosec (`make seccheck`), gitleaks over
the whole history (`make secrets`) and govulncheck over each shipped module (`make vulncheck`). The
first three run in CI on every pull request. govulncheck runs weekly and on each release tag,
because the advisory database moves independently of the code. OpenSSF Scorecard runs weekly and on
each push to main. The query parser and the evaluators are also fuzzed (`./selfcheck.sh`).
