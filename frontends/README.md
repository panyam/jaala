# frontends

What turns text other than jaala's own syntax into a program that checks. Nothing here is needed to
run the engine, and no engine-only host imports it. Each front end is a module of its own, so no
core package can import one and what one depends on reaches only its importers; run its tests from
inside its directory.

- `nl/` asks a model to write a program from a plain-language question (#321): the vocabulary as a
  card, a grammar per vocabulary for constrained decoding, errors a model can fix from, the
  translate loop and the eval that scores it. It imports `ns` and `datalog`.
- `nlmodels/` is a module of its own (#330) with clients for local models already running:
  llama.cpp's `llama-server`, which takes the grammar, and Ollama, which can't.

- `cypher/` is a module of its own (standard library and jaala only) that compiles Cypher into a
  program (#352, mission #356) over a `Graph`, a host's view of
  its relations as nodes, relationships and properties. It refuses what a program can't express
  yet by the gap's issue (#279 to #289). It imports `ns` and `datalog`; its tests score it with
  `nl.Eval` on the guide's questions (`internal/guide`), the ones `nl`'s `TestEval` asks.
