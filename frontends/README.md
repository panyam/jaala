# frontends

What turns text other than jaala's own syntax into a program that checks. Nothing here is needed to
run the engine, and no engine-only host imports it.

- `nl/` asks a model to write a program from a plain-language question (#321): the vocabulary as a
  card, a grammar per vocabulary for constrained decoding, errors a model can fix from, the
  translate loop and the eval that scores it. It imports `ns` and `datalog`.
- `nlmodels/` is a module of its own (#330) with clients for local models already running:
  llama.cpp's `llama-server`, which takes the grammar, and Ollama, which can't.

A Cypher or GQL front end (#278) would sit beside `nl`, reusing its card, `Explain` and the
engine's `Locate`.
