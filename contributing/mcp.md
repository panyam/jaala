# The MCP server

`frontends/mcp`.

- **`frontends/mcp` is a module of its own too** (#347), on mcpkit, so its dependencies reach no
  host that doesn't serve agents; CI's `mcp` job tests it, and `SUB_MODS_TO_TAG` tags it. `mcp.New(v,
  pack, opts)` registers `query` (rows with witnesses, paged by an offset `page_token`, errors as tool
  errors carrying `nl.Explain`'s text), `schema` (the card, filtered by a module's or relation's
  path, and the pack's `Card` at `data`), `explain` (`Report.String`) and, with `Options.Translator`
  (an `mcp.Translator`, which nlmodels' `translator.Translator` is, so this module imports no model
  client; the docsite's `mcp_test.go` holds the two to it, and mcp's own tests use a Session loop of
  their own, `sessions`), `ask` (the translator with the pack, `mode` local/frontier/both, panyam/declaire#300, and
  `language` datalog/cypher, #386: Cypher through `cypher.NewFrontEnd` with `Pack.CypherExamples`,
  `Pack.AskLanguage` the default, answered as the `cypher` tool answers). The
  instructions are written from the pack (`mcp.Instructions`, sent with mcpkit's `WithInstructions`,
  which mcpkit v0.8.2 put on the initialize result where clients read it: before, Claude Code saw
  none) unless `Pack.Instructions` replaces them word for word, which declaire#308's wording runs
  use; `Pack.ToolDescriptions`, `ArgDescriptions` and `AgentLine` replace the rest of its text the same
  way (#485), an unknown tool or argument name failing `New`. `Options.Abstain` (#482) answers an abstained `ask` as an ordinary result, not a tool error. `Options.Server` (#474) are a host's own mcpkit options, applied after those, so the host's
  wins, a `WithInstructions` included (agni's `WithListTTLMs`; unset, mcpkit sends `ttlMs: 0`, not nothing). A host adds its own tools on the returned server and writes answers with `Pack.Format`, and
  `Pack.Data` (#469) as structured content, which must be a JSON object and replaces the `json` shape.
  `query` and `explain` take `bind` (`bind.values`, then `option` into `datalog.Bind`; a list ranges a
  variable), and `Answer.Bind` reports it, or cypher's `params`, by the name the call gave (#472). Its
  own arguments go on jaala's tools through `mcp.NewWith` and an `Extension[A]` (#357): `A`'s
  fields are merged flat into each tool's schema (`args.go`, which reads the arguments twice, since
  Go can't embed a type parameter), a clash with a tool's own name is refused at startup, and every
  schema says `additionalProperties: false`, so mcpkit's argument validation fails a misspelled
  call, plain `New`'s too. `Prepare` returns a `Run` per call (Base, Eval, a whole Pack, a `Vocabulary`
  the vocabulary-reading tools use instead of the server's, never resolving it, #469, a `BaseFor`
  asked with each program query, explain, values and cypher run, in place of Base, ask excepted, #472, and a
  `Timeout` that shortens `Options.Timeout`, #361: the handler in `args.go` sets the deadline on
  `ToolContext`'s embedded context before `Prepare`, and `budgetHint` names it). `look`, `values` and
  `find` (`actions.go`, #393) are nl's actions as tools, through the exported `nl.Look` and `nl.Probe`,
  with `language` datalog or cypher; `find` is registered only for a pack with a Grounder. `sample` on
  `query` and `cypher` lowers the program's own limit and, when nothing comes back, sets `Answer.Why`
  from an Explain report (`whyEmpty`). `Options.Vocabulary`
  replaces `New`'s vocabulary (`New(nil, …)`; both or neither is refused) and is resolved under a lock
  by the first call to a tool in `readsVocabulary` (schema, ask, cypher), running New's pack checks
  (`checkVocabulary`) then; a failure isn't kept. Its
  tests are a toy host (`roads`) through mcpkit's `testutil` client. A pack with a `Cypher` graph
  (#366) adds a `cypher` tool: it compiles over the call's pack (so `Run.Pack` brings its own graph,
  and one without refuses), answers as `query` does with `ran:` and the program first, and names
  its columns as Cypher did (`Answer.Columns`, `Answer.Cypher`; `query` fills `Columns` too). Its
  refusals, compile or check or run, are `cypher.Explain`'s (#353): `Program.Locate` maps a check
  error back to the Cypher that wrote the failing piece, since the compiler wraps each piece in an
  `origin` fragment that `render` marks with three control bytes `chooseMarks` picks from those no
  input holds (fuzzing found a query string holding `\x01`), and `write` strips them into spans. `cypher.Store` writes the graph of a
  `node(id, kind)`, `edge(from, kind, to)`, `attr(id, key, value)` store, one label per kind
  (`DefaultLabel`, `DefaultType`), reading the kinds with `Kinds`; mcp's tests write their graph by
  hand, and `Store` is tested in `frontends/cypher`.
