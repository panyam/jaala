# frontends

What turns text other than jaala's own syntax into a program that checks. Nothing here is needed to
run the engine, and no engine-only host imports it. Each front end is a module of its own, so no
core package can import one and what one depends on reaches only its importers; run its tests from
inside its directory.

- `nl/` asks a model to write a program from a plain-language question (#321): the vocabulary as a
  card, a grammar per vocabulary for constrained decoding, errors a model can fix from, the
  translate loop, a host's domain pack (`nl.Pack`, `nl.DataCard`) and the eval that scores it. It
  imports `ns` and `datalog`.
- `nlmodels/` (#330) has clients for local models already running: llama.cpp's `llama-server`,
  which takes the grammar, and Ollama, which can't. It imports `nl`.
- `mcp/` (#347) is an MCP server for any vocabulary, on mcpkit: `query`, `schema`, `explain` and,
  with a model, `ask`, its instructions written from a host's `nl.Pack`, and a host's own arguments
  on every tool (#357). A pack mapping its graph (`Pack.Cypher`) adds a `cypher` tool (#366), so mcp
  imports `cypher` too.
- `cypher/` (standard library and jaala only) compiles Cypher into a
  program (#352, mission #356) over a `Graph`, a host's view of
  its relations as nodes, relationships and properties, which `Store` writes for a node, edge and
  attr store (#366). It refuses what a program can't express
  yet by the gap's issue (#279 to #289). It imports `ns` and `datalog`; its tests score it with
  `nl.Eval` on the guide's questions (`internal/guide`), the ones `nl`'s `TestEval` asks.
