# jaala

[![ci](https://github.com/panyam/jaala/actions/workflows/ci.yml/badge.svg)](https://github.com/panyam/jaala/actions/workflows/ci.yml)
[![docs](https://github.com/panyam/jaala/actions/workflows/docs.yml/badge.svg)](https://panyam.github.io/jaala/)
[![Go Reference](https://pkg.go.dev/badge/github.com/panyam/jaala.svg)](https://pkg.go.dev/github.com/panyam/jaala)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/panyam/jaala/badge)](https://scorecard.dev/viewer/?uri=github.com/panyam/jaala)

Jaala (Sanskrit for "network") is a Datalog engine for graph-shaped data that you embed in a Go
program. You hand it facts and ask questions as rules and a goal, and every answer comes back with
the facts that produced it.

```
reach(?a, ?b) :- edge(?a, ?b);
reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c);
reach("a", ?x) => ?x
```

Over the edges a→b, b→c and c→d, that answers `b`, `c` and `d`, and the row for `d` cites the three
edges on the way there.

**Documentation: [panyam.github.io/jaala](https://panyam.github.io/jaala/).** Every example on the site
is run on the engine when the site is built, so what it shows is what jaala answers today. Start with
[what jaala is](https://panyam.github.io/jaala/overview/), then the
[tutorials](https://panyam.github.io/jaala/tutorials/), which end with
[embedding jaala in Go](https://panyam.github.io/jaala/tutorials/05-go-host/).

## What it does

- The language has recursion, stratified negation, aggregation in answers and in rules, optional
  matches, arithmetic with units, paths and closures, lists, and types that refuse a wrong constant
  rather than match nothing ([syntax](https://panyam.github.io/jaala/guide/syntax/),
  [the guide](https://panyam.github.io/jaala/guide/)).
- Each answer row carries its facts' citations, and on request the tree of rules and facts that
  derived it ([provenance](https://panyam.github.io/jaala/architecture/provenance/)).
- A query costs about the same however its rules are written, since the engine rewrites them for
  what the goal demands and plans each body, under a budget the host sets. `Explain` says where the
  work went ([evaluation](https://panyam.github.io/jaala/architecture/evaluation/),
  [rewrites](https://panyam.github.io/jaala/architecture/rewrites/)).
- Facts can stay in your own stores. A store answers only what a query binds, and can join, count,
  page and walk for itself; several stores federate as one, and a graph kept in Neo4j or Memgraph
  works as is ([facts from stores](https://panyam.github.io/jaala/guide/stores/)).
- One base answers at any version of its facts, across all of them at once, or says what changed
  between two, and a session keeps edits over a store
  ([versions](https://panyam.github.io/jaala/guide/versions/)).
- You can also ask in Cypher, which compiles to a program
  ([asking in Cypher](https://panyam.github.io/jaala/guide/cypher/)), have a model write one from a
  plain-language question ([asking through a model](https://panyam.github.io/jaala/guide/plain-language/)),
  or put any vocabulary in front of an agent as an MCP server
  ([serving agents](https://panyam.github.io/jaala/guide/mcp-server/)).

It started as the query engine inside [agni](https://github.com/panyam/agni), an EDA tool, and was
pulled out so other graph tools could share it. [Declaire](https://github.com/panyam/declaire) is the
second host.

## What's in the repository

The engine is three packages in the root module, layered one way
([layering](https://panyam.github.io/jaala/architecture/layering/)). They import only the Go
standard library and build for WebAssembly, since agni runs jaala in the browser.

| Path | What it is |
|---|---|
| `ns` | The vocabulary: every name a query can call and its signature, the `Source` interfaces a store implements, versions and sessions. A host's fact layer imports it without taking on an engine. |
| `stdlib` | The standard vocabulary: `str.*`, `num.*` and units, `list.*`, `path.*`, `absent`. |
| `datalog` | The engine: parsing, modules, checking, the evaluators, and what a store is asked. |

Everything else is a Go module of its own, so its dependencies reach only the hosts that import it:

| Module | What it is |
|---|---|
| `frontends/cypher` | Compiles Cypher into a jaala program over a host's graph. |
| `frontends/nl` | Turns a plain-language question into a program: the vocabulary as a prompt, a grammar for constrained decoding, errors a model can fix from, and an eval. |
| `frontends/nlmodels` | Clients for local models (llama.cpp, Ollama). |
| `frontends/mcp` | An MCP server for any vocabulary. |
| `stores/bolt` | A source over Neo4j or Memgraph, with joins, counts, pages and walks run on the server. |
| `docsite` | The documentation site ([how to build it and add to it](docsite/README.md)). |

```sh
go get github.com/panyam/jaala@latest                    # the engine
go get github.com/panyam/jaala/frontends/cypher@latest   # or any module above, tagged with it
```

Every module is tagged at the same commit as each release. jaala is pre-1.0, and a release that
changes what a host must write says so on the issue that host filed.

## Developing

`go test ./...` runs the root module's tests, and each sub-module's run from inside it. CI runs
those, a generated corpus of random programs checked across three evaluators, the same corpus
against [Soufflé](https://souffle-lang.github.io/), the docs, and the scanners below. The
[testing page](https://panyam.github.io/jaala/architecture/testing/) explains how answers are checked.

| Command | What it runs |
|---|---|
| `./selfcheck.sh`, `make selfcheck` | Everything at full size: 5000 corpus seeds, fuzzing, the Soufflé and graph-server differentials. `make selfcheck-status` says when one is due. |
| `make audit` | staticcheck, gosec, gitleaks and govulncheck over every shipped module. |
| `make -C docsite check` | Builds the site and runs every example on it. |
| `make setup` | Installs Soufflé and llama.cpp for selfcheck. |

[CLAUDE.md](CLAUDE.md) holds the project's constraints and conventions in detail.

## Security and license

Report a vulnerability privately, as [SECURITY.md](SECURITY.md) describes. jaala is licensed under
[Apache-2.0](LICENSE).
