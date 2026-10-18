---
title: "Serving agents"
description: "An MCP server for any vocabulary: the query, schema, explain, ask and cypher tools, instructions written from the host's pack, a host's own arguments and tools, and how a host runs it."
prev: {url: "/jaala/guide/plain-language/", title: "Asking through a model"}
next: {url: "/jaala/guide/cypher/", title: "Asking in Cypher"}
---

An agent with its own tools reaches for a graph tool only when it knows what the tool is for. Declaire's agent called Declaire's server on 1 of 42 questions until one sentence told it, and then on the hard ones ([declaire#288](https://github.com/panyam/declaire/issues/288)). `github.com/panyam/jaala/frontends/mcp` is an MCP server for any vocabulary, whose instructions and tool descriptions are written from the host's [pack]({{.Site.PathPrefix}}/guide/plain-language/#domain-packs) ([#347](https://github.com/panyam/jaala/issues/347)), so wording that works for one host's agent works for every host's. We keep it in a module of its own on [mcpkit](https://github.com/panyam/mcpkit), so a host that doesn't serve agents never takes mcpkit on.

The examples use the vocabulary from [Host options]({{.Site.PathPrefix}}/guide/host-options/), as the previous page does.

```go
{{ includeFileText "examples/guide/mcp_test.go" }}
```

## Building and running it

`mcp.New(v, pack, opts)` takes the vocabulary, an `mcp.Pack` and `mcp.Options`, and returns mcpkit's `*server.Server`. It refuses a pack whose examples don't check against the vocabulary, and options without a `Base`.

- `mcp.Pack` is an `nl.Pack` plus what only a server needs: `Name` and `Version`, which identify it to a client (`jaala` and `0.0.0` when empty), `Instructions`, and `Format` (below).
- `Options.Base` is a func asked once per call, so a host can pick a workspace or a store per caller.
- `Options.Limit` is the most rows a call returns (100 when 0) and `Options.MaxWork` caps every call's work (no cap when 0). A call may ask for less of either, never more.
- `Options.Timeout` is the longest a call may take, `Prepare` and `Base` included ([#361](https://github.com/panyam/jaala/issues/361)). A call past it stops and answers a tool error saying how long the server gives one, and a `Run`'s `Timeout` may shorten it for one call but never lengthen it.
- `Options.Vocabulary`, given in place of `New`'s vocabulary (`mcp.New(nil, pack, opts)`), resolves it on the first call that reads it (#361). A host whose vocabulary comes from its data, such as relations named after the edge kinds a workspace holds, answers `initialize` at once instead of opening the workspace first, which a cold cache can take longer than a client waits to connect. `schema`, `ask` and `cypher` wait for it, while `query` and `explain` read the `Base` and never do. A failed resolve is the call's tool error and is tried again on the next, and the checks `New` makes of the pack (its examples, the card, the Cypher graph) run on the first vocabulary it returns, so a broken pack fails that call rather than startup.
- `Options.Eval` are options every evaluation runs with, such as `Witnesses()`.
- `Options.Model` offers the `ask` tool, and `Options.Fallback` is the model it asks after `Model`, as in `nl.Options`.

Transport and sessions are mcpkit's, so picking one is pretty much a line in the host's `main`. The second example above serves over HTTP with `srv.Handler(server.WithStreamableHTTP(true))`, as `srv.Run(addr)` does, and an agent that starts the server itself, such as Claude Code from its MCP settings, talks to `srv.RunStdio(ctx)` instead.

## The tools

- **`query`** runs a `program` and answers a page of rows, each with the facts it was derived from. `limit` asks for fewer rows than the server's limit, and `max_work` for less work than its cap. An answer longer than a page ends with a `page_token`, which the next call passes to continue; the token is the offset of the next row, and each page runs the program again, so the server keeps nothing between calls. `json: true` answers with the columns, rows, witnesses, total and token as JSON instead of text.
- **`schema`** answers the card with no `path`, a module's members or a relation's signature, doc and rules with one, the pack's `Card` at `data`, and the graph the `cypher` tool reads at `cypher`.
- **`explain`** runs a `program` under `max_work` and answers its `Report` instead of its rows: where the work went and, for an empty answer, the literal that matched nothing.
- **`ask`**, offered only when `Options.Model` is set, translates a `question` through `nl.Translate` with the pack and answers the program, its rows and each try. Its `mode` is `local` (the model alone), `frontier` (the fallback alone) or `both`, the default. It takes `limit`, `max_work` and `json` as `query` does.

A program that doesn't parse, check or finish within its budget is a tool error carrying `nl.Explain`'s text, as the third call above shows, so the agent fixes the program and calls again. Every tool's schema allows only the arguments it names, so a misspelled argument is a tool error too, never ignored.

## The cypher tool

Agents know Cypher far better than jaala's Datalog. A pack whose `Cypher` field maps the vocabulary as a property graph (a [`cypher.Graph`]({{.Site.PathPrefix}}/guide/cypher/#the-graph-view), which `cypher.Store` writes for a node, edge and attr store) gets the `cypher` tool too, so an agent can ask in the language it knows ([#366](https://github.com/panyam/jaala/issues/366)). It's a tool of its own rather than a language argument on `query`, so its name tells an agent Cypher works before it reads anything else, and its description names the graph's labels and relationship types. `New` refuses a graph that doesn't fit the vocabulary.

- It takes a Cypher `query`, `params` for its `$parameters` (a string or a number each), and `limit`, `page_token`, `max_work` and `json` as `query` does, plus the host's own arguments.
- It compiles the query over the call's pack, so a `Run`'s pack can bring a graph of its own for the call, and a pack without one refuses the call. The program it compiled to then runs exactly as `query` runs a program, with the same base, budget, witnesses and paging.
- The answer opens with `ran:` and the program, as the last call in the example above shows, so an agent sees what ran and can hand it to `explain`. Its columns are named as the query named them, an alias or `p.name`, and `Pack.Format` gets the Cypher text and those names in `Answer.Cypher` and `Answer.Columns`. With `json` the answer carries `cypher` and `program` too.
- A query the compiler refuses, or whose program doesn't check or run, is a tool error written by [`cypher.Explain`]({{.Site.PathPrefix}}/guide/cypher/#errors-in-the-querys-terms): the message in the query's terms, the line of the query with a caret under the place, the fix, and for Cypher a program can't express yet, the issue that would let it, so the agent rewrites rather than retries. An error from checking or running the program also shows the program it ran.

## Instructions

The instructions an agent reads when it connects open with the pack's `About`, say how to write a program and to call `schema` first, show the pack's first two examples, and, with a graph, say Cypher works and name its labels and types, as the first example above prints; `mcp.Instructions` returns them. `Pack.Instructions` replaces them word for word, for a host trying wordings against its own agent ([declaire#308](https://github.com/panyam/declaire/issues/308)). `mcp.AgentLine` is a sentence for the host's agent instructions, such as a CLAUDE.md, saying when to use the server.

## A host's own tools and arguments

A host registers its own tools on the server `New` returns, with mcpkit's `RegisterTool`, and writes answers its own way with `Pack.Format`, which gets each page as an `mcp.Answer`: the program, the whole `Table` with every row's witness, the page's offset and length, and the next token.

A host's own arguments go on jaala's tools through `mcp.NewWith(v, pack, opts, mcp.Extension[A]{Prepare: …})` ([#357](https://github.com/panyam/jaala/issues/357)). `A` is a plain struct tagged as a tool's input is (`json`, `jsonschema_description`), such as the repos a query reads or how many lines of code an answer shows, and its fields sit flat in every tool's schema beside the tool's own `program` or `limit`. On each call `Prepare` gets the tool's name and the decoded `A`, and returns an `mcp.Run`: the `Base` the call reads, more `Eval` options, and a `Pack` that replaces the server's for the call, so its `Card` is what `schema` says at `data`, its `Format` writes the answer and `ask` translates with it. A field left empty is the server's. An error from `Prepare` is the call's tool error. `NewWith` refuses an `A` that isn't a struct or whose names clash with any tool's own; `New` is `NewWith` with no host arguments.
