---
title: "Layering"
description: "The three packages, ns, stdlib and datalog, and why each imports only the one below it; the front ends, each a module of its own above them; and what stays with a host."
next: {url: "/jaala/architecture/evaluation/", title: "Evaluation"}
---

jaala is three Go packages, and they import each other in one direction only:

- **`ns`** is the contract. It holds the vocabulary's tree of names, values and their types, signatures, the `Source` a host serves facts through (and `Federation`, one Source over several stores), and the hook that lets a language like Datalog define modules, and it imports nothing else in jaala.
- **`stdlib`** is the standard vocabulary, with the string tests, `absent`, arithmetic and units (`num.*`), and the glob and regex compilers. It imports `ns` and never the engine.
- **`datalog`** is the engine, which parses, links, checks, infers and evaluates. It imports `ns` only, so even the standard library is something a host adds rather than something the engine assumes.

We keep the direction strict for a fairly practical reason. A host's fact layer, the code that turns a netlist or a repository into facts, imports `ns` to describe its relations, and it shouldn't have to take on a query engine to do it. agni's own architecture depends on that split. A test at the module's root, `TestLayering`, fails if any import runs the wrong way.

## Standard library only

All three use only the Go standard library, and CI checks it on every change with `go list -deps`. Anything jaala imports, every host imports too, and agni runs jaala in the browser, where a dependency is download size. The same reason keeps everything building for `GOOS=js GOARCH=wasm`, which CI also checks, and which is how the examples on this site run in your browser.

## Front ends

What turns other text into a program sits above the three packages, under `frontends/`, and nothing in them imports it. Each front end is a Go module of its own, so its dependencies reach only its importers: `frontends/nl` (the plain-language front end) and `frontends/cypher` import `ns` and `datalog` and nothing beyond the standard library, `frontends/nlmodels` imports `nl` for the models it implements, and `frontends/mcp` takes on mcpkit for its server. `TestLayering` checks what `nl`, `cypher` and `nlmodels` import as it does the packages below, and a front end is tagged beside jaala at each release (`frontends/nl/v0.1.N` with `v0.1.N`). A store a host could use as is sits under `stores/` the same way: `stores/bolt` takes on the Neo4j driver, and nothing in jaala imports it.

This documentation site is its own Go module under `docsite/`, so the site generator and its dependencies never reach jaala's. It replaces jaala and every front end with the checkout, so its examples run on the code beside them.

## What stays with a host

The same question comes up one level higher, between jaala and the hosts built on it. jaala's job is to give an agent deterministic primitives, and the MCP tools over them, that let it answer accurately for the least money and time per correct answer, a cheap model included. Each domain's host wraps jaala with its own knowledge and ingestion: Declaire for code, agni for schematics. A capability belongs in jaala when it means the same thing in every domain, and with the host when knowing the domain is what makes it work.

| jaala (the same in every domain) | the host (Declaire, agni) |
|---|---|
| The engine: evaluation, planning, stores, budgets | The vocabulary, what its relations are and mean |
| Change sets, versions, `At`, `Diff` and keyed updates | Identity, what makes two things one (a symbol id, a refdes) |
| Ingesting text with checks and spans (`nl.Extract`) | Ingesting structure (go/types and SCIP, a netlist), which stays Go |
| Primitives any domain can use: aggregates, walks, the text functions | Its fast paths, as generators, and the model behind a similarity it needs |
| The agent's surface: the MCP server, `ask`, actions, `Explain` and `Fix` | The pack (card, examples, hints, keys) and any tools of its own (`mcp.NewWith`) |
| The measuring: `nl.Eval` and cost per answer | The benchmarks its users care about |

Two rules of thumb decide the rest. A capability moves into jaala once a second host needs it, or sooner when it is plainly free of any domain (dates, say). And where a primitive needs a model or a domain to implement it, jaala defines the contract and the host supplies the implementation, as `stdlib.Hops` walks a host's own step function. That keeps jaala on the standard library, so one host's dependencies never reach another's. Epic [#455](https://github.com/panyam/jaala/issues/455) tracks which primitives are still missing on jaala's side.
