# Contributor notes

What each part of jaala is, and where the notes on it live. The root `CLAUDE.md` routes here.

A Datalog engine for graph-shaped data, extracted from agni. Three packages: `ns/`, the contract
(the namespace tree, value and signature types, members, suggestions, the `Language` hook);
`stdlib/`, the standard vocabulary (`str.*`, `num.*`, `absent` and `coalesce` (#284), the glob and regex compilers); and
`datalog/`, the engine (parsing, linking, `Check`'s resolution and inference, evaluation). Read
`ns/doc.go`, then `datalog/doc.go`. `frontends/` holds what turns text other than jaala's syntax into a
program that checks, and no engine-only host imports it: `frontends/nl`, the plain-language front end
(#321), and `frontends/nlmodels`, its clients for local models in a module of their own (#330), and `frontends/mcp`,
an MCP server for any vocabulary in another (#347); and `frontends/cypher`, which compiles Cypher
into a program (#352), in a third. Every front end is a module of its own, `frontends/nl` included
since #359, so no core package can import one and what one depends on reaches only its importers;
the root's `./...` stops at each, so run a front end's tests from inside it. They moved there in #337, after v0.1.65 had shipped
`nl` and `nlmodels` at the root. `stores/` holds sources a host can use as is, each a module of its own
the same way: `stores/bolt` (#411), a Source over Neo4j and Memgraph. jaala is a Datalog engine, not a graph library: a host's fast
path is a generator (its own Go running inside a query), not a jaala function called around the
engine (#41).

`docsite/` is the documentation site (#104), an s3gen site in its own Go module so its dependencies
never reach jaala's. `docsite/README.md` says how to add a page and an example; every example runs on the engine at build
time and in `docsite`'s tests (`demo.Run`, #106), so an engine change that changes an answer the docs
pin fails the docs workflow. `make -C docsite check` builds it and runs its tests, and `.github/workflows/docs.yml` runs that on every PR and deploys `main` to Pages. A change to a store interface or to how
the planner asks a store also updates `guide/stores.md` (what a host implements) and
`architecture/stores.md` (what the engine asks, and when); one to `ns.Builtin` updates
`guide/predicates.md`. `guide/host-options.md` is for the `Eval` options and sharing a base, not
stores, which is why it was split (#320).

## Where the notes live

- [engine.md](engine.md): the engine's constraints, values, parsing, answers and engine walks.
- [rewrites.md](rewrites.md): Explain, citations, aggregates, arithmetic, units, demand, the derived cache, inlining and planning.
- [stores.md](stores.md): lookups, streams, batches, overlays, federations, versions, pushdown and walks.
- [nl.md](nl.md): `frontends/nl`, its grammars, `frontends/nlmodels` and the translator, and front ends in other languages.
- [mcp.md](mcp.md): `frontends/mcp`.
- [cypher.md](cypher.md): `frontends/cypher`.
- [bolt.md](bolt.md): `stores/bolt`.
- [testing.md](testing.md): running the checks, selfcheck, the testing discipline, the corpus, Soufflé, fuzzing and fixtures.
- [releasing.md](releasing.md): tagging, bump-root, and working with branches and PRs.
- [missions.md](missions.md): how issues are ranked, and the missions' history.
- [hosts.md](hosts.md): working with agni and Declaire.
