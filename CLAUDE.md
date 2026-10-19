# jaala

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
`nl` and `nlmodels` at the root. jaala is a Datalog engine, not a graph library: a host's fast
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

## Commands

`./selfcheck.sh` runs the host-free checks at full size: the generated corpus at 5000 seeds, the
work baselines, fuzzing, the same 5000 seeds against Soufflé when `souffle` is on PATH, and a real
`llama-server` under nl's grammar when `make llama` has fetched it (each prints "could not run"
otherwise). The corpus step takes about 5.5 minutes alone and runs under `-timeout 30m`, as the
Soufflé step does (#381: under a load near 28 it passed go test's default 10 and failed). `make setup` installs both. Every Makefile recipe is a one-line call into `scripts/`
(after mcpkit's): put a new target's shell there, never in the recipe. CI (`.github/workflows/ci.yml`) runs the set below, and a
separate `souffle` job installs Soufflé 2.5's release `.deb` and compares 1000 seeds; all must pass:

```sh
gofmt -l .                      # must print nothing
go vet ./...
go test -race -count=1 ./...
go test -count=1 ./datalog -run '^TestGeneratedProgramsAgree$'   # the corpus skips under -race (#143)
GOOS=js GOARCH=wasm go build ./...
go list -deps ./... | grep '\.' | grep -v '^github.com/panyam/jaala' | grep -v '^vendor/'   # must print nothing
```

## Constraints

- **Standard library only, and it must build for wasm.** agni runs the engine in the browser, and
  any dependency here becomes every host's dependency. The deps check greps for a dot, so a
  standard package can trip it too: `crypto/sha256` pulls in `crypto/internal/entropy/v1.0.0`.
  Run the `go list -deps` line before pushing a new standard import.
- **The packages layer one way: `ns` imports nothing in jaala, `stdlib` imports `ns` and never
  `datalog`, production `datalog` imports `ns` only.** A host's fact layer imports `ns` and
  `stdlib` precisely because it may not import an engine (agni's C29), and the engine doesn't
  depend on the standard vocabulary. `TestLayering` (module root) guards it.
- **Error text keeps its `query:` prefix, and existing fragments stay stable** (`unknown relation
  "x"`, `takes N args`, `not stratifiable`). agni prints these messages and its tests match on
  fragments of them. New cases get new wording; existing wording doesn't move. A new message also
  goes in the docsite's error catalogue (`content/reference/errors.md`), or `make -C docsite check`
  fails (`TestErrorCatalogueListsEveryMessage`).
- **A check error is a `*CheckError`** (`checkerror.go`, #321): a `Kind`, what it is about
  (`Relation` and `Args`, `Var`, `Rule`) and a `Fix` when the engine can tell, its message unchanged.
  A Query carries no positions (the slicer, parse and fuzz tests compare Queries with `DeepEqual`),
  so `Locate(text, err)` finds the call or variable in the text, and `nl.Explain` writes it for a
  model. A value error says which argument (`Arg`, with the value kept unexported) and a comparison's
  type error its constant, so `Locate` finds the call holding that value, the variable at its
  comparison, or for a call arithmetic wrote (`num.mul` for `?a * ""`) the constant itself (#353). A new check builds one (`checkErr(kind, msg)`, `b.arityErr`, `b.unknownErr`), and
  `TestBrokenProgramsFailTheSameWay` requires each broken literal's error to be one Locate finds.
  `nl` (the card, Explain, `Translate`) imports `ns` and `datalog`, is a module of its own (#359; CI's
  `nl` job), and nothing in the root imports it.
  `Translate` (#325) runs no model: `Model` and `Grounder` are the host's, a refused draft goes back
  with `Explain`'s text, a `Complete` error, a repeated draft (#340, refused unchecked) or an empty
  answer (over `Options.Base`) moves straight to the fallback, which starts a fresh conversation (an
  empty answer with no fallback left goes back to the model), and `Try.Kind` adds `parse`, `eval`,
  `empty`, `repeat` and `model` to a `CheckError`'s kinds. An unknown call's `Fix` names the program's
  own rule it likely means (`localFix`, #341, at `Validate` and `evaluate`, since the vocabulary's
  suggestion can't see the rules). Its tests script the model (`scripted`), so tries are exact.
  A draft run over the Base keeps its `datalog.Report` (`Try.Report`), and an empty answer's error
  is an `EmptyAnswer` carrying `Report.Empty`, whose text goes back to the model (#343);
  `nl.Report.Costliest` ranks drafts by work, and its work column shows only when one was evaluated.
  A host's domain is an `nl.Pack` (`Options.Pack`, #344): `About`, `Card`, `Examples` (checked by
  `Pack.Validate` before any model is asked), a `Grounder` used when Translate's is nil, and a `Check`
  that refuses an answer as `answer`, back to the same model. Its `Prefix()` follows the card and the
  instructions. `nl.DataCard(ctx, b)` writes the generic half of `Card` with one counting program per
  argument (named variables, so `count` counts facts); #347's MCP server reads the same pack.
  `nl.Eval` (#326) scores a `translate` func over `Case`s, matching a row by its values as a multiset
  (the model picks the columns' names and order) and rows as a multiset unless `Ordered`. `TestEval`
  is #322's exercise until Declaire's arm runs: the guide's vocabulary, a scripted small model and
  fallback (`byQuestion`), and the report in `frontends/nl/testdata/eval.golden` (`-update`).
- **`nl.Grammar()` is GBNF built from a rule table** (`frontends/nl/grammar.go`, #323), and every text it
  accepts must parse: `TestEveryProgramTheGrammarWritesParses` samples 21,000 programs from the table
  over 70 seeds and every depth (one seed alone missed `-(9)` as a column). The other direction lives
  in `datalog`'s tests, which can reach the corpus: they read `frontends/nl/testdata/program.gbnf` (`go
  test -update .` in `frontends/nl` writes it) with a small Earley recognizer and require every corpus, fuzz-seed and docs
  program, printed in jaala's spelling, to be accepted. A grammar change regenerates the file, or
  `datalog` checks the old one. The card is memoized on the vocabulary (`Memo`). A name is capped at
  `nameMax` (32) characters (#340: a model writing prose ran one to its token limit), written as
  GBNF's `{m,n}`, which `internal/gbnf` expands into nested optionals: flat `x? x?` made Earley five
  times slower.
- **`nl.GrammarFor(v)` is that table with the calls replaced by v's members** (`vocabgrammar.go`,
  #324): each at the arities it takes, a closed domain's argument as a variable or one of its values,
  and a program's own rules named by `local`, a trie keeping them off every root name, whose tails (`lt<n>`) keep a local within `nameMax`. `GoalOnly()`
  drops rules. Its soundness test also checks each sample against `Grammar()`, so it stays a
  sublanguage. The recognizer lives in `internal/gbnf` for both packages' tests, beside the table
  types, `Render` and the `Sampler` (#354, shared with the Cypher grammar; nl keeps its own short
  types and converts with `table`, since vet refuses unkeyed literals of another package's structs,
  and `program.gbnf` must come out byte-identical). `Translate` sends
  it as `Prompt.Grammar`.
- **`frontends/nlmodels` is a module of its own** (#330): thin clients for local models already
  running, `llamacpp` (sends the grammar, `cache_prompt`) and `ollama` (no GBNF, so no grammar).
  CI's `nlmodels` job tests it; the root's `./...` doesn't reach it. Like every
  sub-module it requires jaala and `frontends/nl` at the last release and replaces both with the
  checkout (Releasing). A consumer must not pair the nl module with a jaala tag up to v0.1.71,
  which holds `frontends/nl` as a package: that is an ambiguous import. Its old path, `github.com/panyam/jaala/nlmodels`, shipped
  once (nlmodels/v0.1.65) with no consumer, and nothing is tagged under that prefix again. `./selfcheck.sh` runs a real
  `llama-server` on llama.cpp's 1 MB test model when `make llama` has fetched it (Homebrew's
  llama.cpp ships no GBNF validator, so a server is the only check llama.cpp reads the grammar): every reply must
  stay inside the grammar as `internal/gbnf` reads it. The model lives outside the checkout, at
  `~/.cache/jaala/stories260K.gguf` (`$XDG_CACHE_HOME` if set, or `JAALA_LLAMA_MODEL`), so every
  worktree and clone uses the one copy; without it the step reports "could not run". `llama-server` exits with "Abort
  trap: 6" when selfcheck's cleanup kills it, after its test, which is not a failure.
- **`frontends/mcp` is a module of its own too** (#347), on mcpkit, so its dependencies reach no
  host that doesn't serve agents; CI's `mcp` job tests it, and `SUB_MODS_TO_TAG` tags it. `mcp.New(v,
  pack, opts)` registers `query` (rows with witnesses, paged by an offset `page_token`, errors as tool
  errors carrying `nl.Explain`'s text), `schema` (the card, filtered by a module's or relation's
  path, and the pack's `Card` at `data`), `explain` (`Report.String`) and, with `Options.Model`,
  `ask` (`nl.Translate` with the pack, `mode` local/frontier/both, panyam/declaire#300). The
  instructions are written from the pack (`mcp.Instructions`, sent with mcpkit's `WithInstructions`,
  which mcpkit v0.8.2 put on the initialize result where clients read it: before, Claude Code saw
  none) unless `Pack.Instructions` replaces them word for word, which declaire#308's wording runs
  use. A host adds its own tools on the returned server and writes answers with `Pack.Format`. Its
  own arguments go on jaala's tools through `mcp.NewWith` and an `Extension[A]` (#357): `A`'s
  fields are merged flat into each tool's schema (`args.go`, which reads the arguments twice, since
  Go can't embed a type parameter), a clash with a tool's own name is refused at startup, and every
  schema says `additionalProperties: false`, so mcpkit's argument validation fails a misspelled
  call, plain `New`'s too. `Prepare` returns a `Run` per call (Base, Eval, a whole Pack, and a
  `Timeout` that shortens `Options.Timeout`, #361: the handler in `args.go` sets the deadline on
  `ToolContext`'s embedded context before `Prepare`, and `budgetHint` names it). `Options.Vocabulary`
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
- **A model can write another language than jaala's** (`nl.FrontEnd`, `Options.FrontEnd`, #354): the
  prompt is the front end's card, instructions and examples (the pack's About and Card still follow,
  its jaala examples don't), a draft is compiled (`Compiled`, whose `Explain` is set even on a
  failed compile) before `Validate`, every refusal goes back through `Compiled.Explain`, and
  `Try.Language`, `Program.Language`/`Compiled` and `Report.Language()` ("passed n/m in cypher")
  record it. The interface is nl's and `frontends/cypher` implements it (`NewFrontEnd`), so cypher
  imports nl and never the other way. `cypher.Grammar(g)` is the subset as a gbnf table narrowed
  to g's labels, types and keys, without CALL or `$parameters` (XOR came back once #377 made a
  chain of them compile in linear time); its test holds every sample to parsing, not compiling (a random query names
  variables nothing matched), and requires the guide's real answers to be accepted instead.
- **`frontends/cypher` compiles Cypher to jaala text over a host's `Graph`** (#352, mission #356):
  `Node` and `Edge` name the relation holding a label or a type, its key or endpoints, `Fixed`
  constants (Declaire's `node(id, "Function")`) and property columns, `Props` a key-value
  relation (`attr(id, key, value)`), and `Derived` a relation or predicate per property for any
  node, one the host computes (#378: Declaire's `name.of(id, name)`), read as `kvAtom` reads `Props`,
  after a label's column and before `Props`; `Store.Derived` carries it. The text goes through `datalog.Parse`, so `Validate`, `Locate`
  and `Explain` read it unchanged. A node is its key; an equality every row holds is written into
  the atom (`pkg("app", _)`) unless something else reads the value, which keeps a variable and a
  comparison (`settle`), so a planner sees constants. A frag's node keys are rendered last for
  that reason, and kept as a frag (`subst`): rendered early, a `$param` key in a rule escaped the
  refusal and matched everything (#352's second PR caught it). An OR becomes a `cypher_match` rule
  per alternative, a WHERE pattern or `EXISTS { }` a `cypher_exists` rule over the nodes it shares
  (each a stand-in, `nodeVar.of`, so reading one's column inside reads it there), `COUNT { }` a
  body aggregate (so a node with no match counts 0), a variable length `cypher_reach` (bounded:
  each length up to 8). Each `WITH` closes a stage (`stage.go`): a `compiler` per stage on a shared
  `world`, the stage a `cypher_with` rule whose head is what it hands on, a carried node keeping
  its labels with its label atom written only when a column is read (`nodeAtm.lazy`). A `UNION` is
  one `cypher_union` relation, a part that aggregates first a `cypher_part` rule. `CALL p(args)
  YIELD` is an atom over a predicate or relation, written after the patterns (Naive runs a
  generator where it's written), each YIELD at the argument its label names. `toLower`, `toUpper`,
  `replace`, `split(s, sep)[i]` and text `+` are str.* calls (`textFuncs`, `isText` reads the
  schema's types). A `$param` is a goal variable (`Program.Bind`), refused inside any rule, or with
  `Params` a constant everywhere. Anything outside the subset is an `*Error` whose `Gap` names the
  issue (#279 to #289, #364 for CALL { } and an order or limit in a WITH or union part), tested by position in
  `TestGapsAreRefusedByName`; its known differences from a Cypher engine (distinct rows,
  reachability rather than trails, a Props read as a match) are in `doc.go`. `TestEval` scores the
  guide's questions in Cypher with `nl.Eval` (`internal/guide` is the fixture both front ends'
  `TestEval` share, `testdata/eval.golden`, `-update`), and `FuzzCompile` (in selfcheck) requires
  any text to compile to a program that validates, or to fail with an `*Error` or a type error
  the query itself makes. It is a module of its own (CI's `cypher` job, `SUB_MODS_TO_TAG`), standard
  library and jaala only, and replaces jaala and nl with the checkout (Releasing); `internal/guide`
  imports no front end (its `Case` is converted to `nl.Case` in each test) so the root never
  requires one. Run its tests from inside it: the root's `./...` stops at the module. The docsite's
  `guide/cypher.md` includes `examples/guide/cypher_test.go`, so the docsite replaces the cypher
  module with its checkout too.
- **`Base` is shared across concurrent `Eval`s.** Anything mutable reachable from it must be
  per-query (the `idb*` fields on Eval's shallow copy), atomic (`work`), or locked (`edbCache`,
  `derivedCache`, the vocabulary's `Memo` entries). `TestConcurrentEvalsShareOneCheck` and
  `TestBasesOverDifferentSourcesEvaluateConcurrently` catch a regression under `-race`.
- **Strategy code lives on its strategy, never on shared state.** `Base` is the fact store plus the
  primitives every evaluator shares (`checkRules`, `applyRule`, `solve`); each evaluator owns its
  fixpoint (`Naive.materialize`, `SemiNaive.materialize`), and SemiNaive's rewrites live in their own
  files. Same-package access to `Base`'s fields is not a reason to add a method to it.
- **Every Eval carries its own run state (`Base.run`: context, budget), on its own copy of the
  Base.** Work is counted through `countWork`, which also checks the budget and, every 1024 units, the
  context; a new loop over candidates must call it and return its error. Host code gets the context
  (`Gen`'s first argument, `ns.ContextSource`), and every emitted generator row counts as work. A
  Source read that fails is not cached.
- **A `LookupSource` is asked, never read whole, for a call with an argument bound** (`lookup.go`,
  #126), unless the Base already holds the relation whole or is `Unindexed`. What it returns lives in
  two tiers: `evalRun.looked`, the Eval's own for its whole run, and the Base's `lookupCache` (#212),
  shared, LRU-bounded in tuples (`LimitLookupCache`, a million by default, 0 off; an empty result
  counts one), dropped with the other caches by a version move or `Forget`, and stored into only in
  the generation the Eval started in (`run.lookGen`). A probed relation is held only as far as it was
  probed and never past the limit. Tests that reuse a Base and count lookups see the second query ask
  nothing: give each its own Base, or `LimitLookupCache(0)`. Anything that sizes
  a base relation (`fanOut`, `scanSize`) checks `looksUp` first and reports "unknown" rather than
  reading it. The corpus runs every program over a `looking` wrapper too (`agreeLookingUp`).
- **A host's own work is charged through `ns.Allowance`** (`ns/allowance.go`, #361). The Eval's
  run is the Allowance on the context every Source call and `Gen` gets (`ns.WithAllowance` in `eval`),
  in `ns` because a host's fact layer may not import the engine. `Spend` adds to `run.spent`, which
  `check` charges beside `used` and `read`, so, as reads, it is in `BudgetExceeded.Work` but not
  `Work`, the baselines or Explain's literal `Work`: `Report.Spent` and `LiteralReport.Spent` show
  it. A host returns `Spend`'s error as it is, and `readFailed`, `lookupErr` and `pushErr` unwrap it
  (`spentAll`) so the Eval fails with the plain `BudgetExceeded`. `Spend` runs on the goroutine the
  engine called the host on, as `emit` does, so `run`'s counters stay plain ints.
- **A relation a `StreamSource` hands over is charged to the budget as it arrives** (#233,
  `readTuples`). Each tuple calls `evalRun.readOne`, which counts `run.read` beside `run.used`
  against the budget, so a budgeted query stops having held at most that many tuples. A read isn't
  work: `Base.Work`, `Report.Work` and `Explain` leave it out, so the work baselines and
  `TestExplainAccountsForEveryUnitOfWork` don't move, and only `BudgetExceeded.Work` includes it. A
  stopped read fails and isn't kept. A Federation always streams (its members in turn, a slice
  member's tuples one by one). A lookup's results still arrive as a slice (#242).
- **A goal that alone reads a streamed relation solves it as it arrives** (`stream.go`, #264).
  Before `magic` (which plans the goal), SemiNaive's rewrite lists in `run.streamable` the base
  relations of a `StreamSource` read in exactly one place, a goal literal, and not held; `fanOut`
  sizes them "unknown", as a looked-up relation, so a tie keeps the written order. `streamFirst`
  marks the goal's first literal (`run.stream`) when its relation is still not held: a rule, a
  demand rule or a prefetch prefix that read it kept it, and the cache is read instead. A relation
  the Source looks up streams only for a call binding nothing under a limit without an order by
  (`stopping`), so a repeated unbounded question still asks the store nothing (#212). `streamEDB`
  normalizes each tuple and makes it a candidate exactly where the cached path's index would (the
  first `indexMinFacts` wait until the size is known), so answers, `Work` and `Explain` match; it
  keeps nothing, so the next query reads it again. A streamed read now interleaves work with
  reading, so a stopped query has done some work. The corpus runs every program over `streaming`
  (`agreeStreamed`); `streamedPrograms` is its control.
- **A `BatchLookupSource` is asked once per looked-up call for all its values** (`prefetch.go`,
  #153, #230). Before solving a body, the planned SemiNaive (`run.batch`, set in its `rewrite`; Naive
  and `WrittenOrder` never batch) runs the body's prefix up to each looked-up literal, and the whole
  positive body once for every negated one, collecting values no tier holds, at most `batchSize` per
  call; `solve` then only hits the cache, so it stays depth-first and #120 holds. A miss is looked up
  as before, so answers never depend on it. It stops at a `Volatile` predicate and skips a goal with a
  `limit` and no `order by`. `LookupBatch` returns one result per value, not a union: a union can't
  tell a sloppy member's whole relation, once per value asked, from two members serving one fact.
  The rerun prefix counts its work against the body with the explainer `quiet`, so no literal counts
  it as reached. `prefetch` only peeks at the Base's cache, so a kept result reaches `solve` and is
  counted `Kept`. A Federation always batches, a member without `LookupBatch` asked per value. A
  recursive rule batches per round, so a walk costs a call per level (`deep-walk`); that's the floor
  without the store walking itself (#231, moved to pushdown, #213).
- **A `Federation` is a Source over several (`ns/federation.go`, #211), so it lives in `ns`, beside
  `MemSource`.** A relation is the union of its members'; a lookup goes only to the members whose
  `Holds` allows the bound values. The engine asks `ns.LookupRelations` per relation (`lookable`),
  and a relation one member can't look up is read whole and kept, never filtered per probe. A fact
  two members serve is two facts, as in one Source serving it twice. The corpus runs every program
  over a three-member federation too (`agreeFederated`), whose limited answers without an `order by`
  are checked as a subset, since the tuples arrive in another order (#194).
- **A version's Base reads the Source its TimedSource serves for it** (`versions.go`, #179, #312).
  `At(v)` runs on a Base kept per version (`versionCache`, `LimitVersions`). Over an `ns.SourceAt`
  it is `NewBase` over `SourceAt(v)`, so lookups, pushes, walks and the rest reach the version's
  store and `TimedTuples` is never read; otherwise it reads v's facts from `TimedTuples`
  (`atSource`, read once for every version). `SourceAt` runs outside the cache lock and an error
  isn't kept. A version's caches are keyed by `versionTag` (the TimedSource's version, v, and the
  version Source's own), carried on `Base.versioned` rather than asserted off `src`, since wrapping a
  host's Source to add `Version` would hide its store interfaces. `Federation.Timed` makes a
  `TimedFederation`: a plain member holds in every version, and at v it is a Federation of the
  members at v (`versionView` for a timed member without `SourceAt`), partitioned when the original
  is. `Diff` still reads `TimedTuples` whole, and `Across` does but for a bound call over an
  `ns.TimedLookupSource` (#317): `acrossBase` gives its Base the `acrossSource` as looker, `lookup`
  returns each row's version sets beside its rows (kept under the lookup key in `acrossSource.looked`,
  since a lookup's rows have no place in a whole read's `sets`), and `unifyEach` takes the sets from
  its caller. `TimedFederation` routes `LookupTimed` by `Holds` (a plain member through `Lookup`, in
  every version) and says per relation in `LooksUpTimed`. Nothing else goes to a store under
  `Across`, and an Across Base is per Eval, so nothing is kept. `agreeAcross` runs every program over
  a `lookingTimed` store too (sloppy every other seed, through a timed Federation every third);
  `acrossLookedPrograms` is its control. The corpus runs `agreeAt` over
  `storing(timed)` too (a `pushing` store per version, through a timed Federation every third seed),
  failing if `TimedTuples` is read; `atStoredPrograms` is its control.
- **A Source may say which lookups it answers from an index** (`ns.IndexedLookups`, #154).
  `planBody` ranks a looked-up call on a pattern the Source says is unindexed as if nothing were
  bound (`scansOnLookup`), so an indexed call runs first; nothing else reads it, so a Source held in
  memory plans as before and the work baselines don't move. A Federation's pattern is indexed only
  when every member serving it is. `agreeFederated`'s stores index only the first argument, so the
  corpus runs the reordering; `TestStoresExercise`'s `callees-with-attr` is its acceptance case.
- **A `PushSource` evaluates a conjunction of a body's base literals** (`push.go`, `ns/push.go`, #237).
  `pushConjunctions` runs after `plan`, in the planned SemiNaive only, and replaces each group the
  Source's `Pushes` accepts with one literal over `\x00p:{…}` whose arguments are the group's
  variables (`run.pushed` holds the group, keyed by a name carrying its comparisons and filters too:
  two rules pushing the same atoms under different comparisons shared one, the second's, and answered
  wrongly from #237 to #297). A group starts at a pushable literal and takes each later
  literal sharing a variable with it and none with a literal outside it before its place, stopping at
  a literal outside it that only filters the group's variables (`filtersGroup`, #291: otherwise the
  store joins every row the filter would drop), unless it is a filter predicate over the group's
  variables whose predicate the store names in `ns.FilterSource`, which goes with it as
  `Conjunction.Filter` (`filters`, #297: Declaire's x05 pushed 67,650 callsites two `str.glob`s cut
  to 21), offered before the comparisons are and regrown with #291's stops when the store refuses it
  (`growPast`). Opt-in on purpose: a store from before #297 accepts a conjunction and ignores the field,
  so growing past filters for it took x04 from 334k rows fetched to 2.1M; one literal
  alone is a group when nothing binds it, it has no constant, and a variable repeats in it or a
  comparison the store takes goes with it (`filtersAlone`, `repeatsVar`, #263); variables
  bound before it are `Inputs` (`offered`), which the store must take (#246), and `prefetchPushed`
  (from `prefetch`) asks for a batch of their bindings (`pushBatch`), filing each binding's rows under
  `pushKey(g.conjunction(bnd))`, where `extendPushed` finds them; a binding not filed is solved literal
  by literal (`solveUnpushed`). Comparisons over its variables go along when the store takes them and
  stay in the body. `extendPushed` unifies every row's tuples with the literals as
  written, so a sloppy row drops and witnesses splice per literal at its `at` (`parts`). Rows are
  charged with `readOne` and kept in the lookup tiers under `pushKey`. A semi-naive round that binds a
  group (`deltaFirst`) solves it literal by literal (`solveUnpushed`); `scanSize` sizes a pushed
  literal 0 so a round keeps starting from it. A Federation of one member pushes what it pushes; one
  built with `NewPartitionedFederation` pushes across members (#238): `pushAcross` asks each member
  `Holds` allows for the first atom with `Partial` (every prefix of its matches too) when another
  member's `Holds` allows a later atom (`continued`, #307; otherwise the conjunction whole), with
  `Inputs` the bindings `routed` sends it (#268, trying `Holds` with only the atom's constants before
  any binding), a conjunction with an atom no member holds is skipped (`heldSomewhere`), and a prefix
  continues only where its next atom is held (`othersHold`). That leans on `Member.Holds`' promise
  that fewer bound values never answer false where more would answer true. Then `continuePrefixes` sends the rest of the conjunction once per
  member for every prefix of as many atoms, their values as its `Bindings` (`restAfter`), joining each
  row back to its prefixes by `joinKey`, which must equate what a member's comparison does; a member
  refusing `Inputs` is asked per prefix (`Conjunction.after` substitutes its values). A match is found
  once and a loose `Holds` costs calls, never answers. One atom goes to each member serving it, in any
  Federation. The engine sees only whole rows. The corpus runs every program over `pushing`
  (`agreePushed`, alternating a store that refuses comparisons) and over a partitioned three-member
  federation dealt by first argument (`agreePartitioned`); `pushedPrograms` (joins),
  `partitionedPrograms` and `partitionedBatchPrograms` are its controls, and `alonePushedPrograms`
  counts `goalQuestions`' lone literals, since generated literals join or carry constants.
- **A goal a store could evaluate whole is summarized or paged** (`goalpush.go`, #240, #241).
  `goalGroup` takes the goal's positive body when it is only pushable base literals (or pushed
  groups) and comparisons over their variables, with no negation. An aggregating goal asks
  `ns.SummarySource` for rows per distinct group and reduced values, a plain `count` needing none,
  plus every variable a comparison reads, which the engine checks per row; `aggregator.addN` feeds a
  row's match count (`exactSum.addTimes` adds a product with its FMA error). A variable only `min`/`max`
  read, and nothing compares, goes as `Summary.Extremes` (#252): per row and unit the store reports
  numbers, NaN and bounds (`ns.Extreme`), fed by `accumulator.addExtremes`; a store refusing them is
  asked for `Values` instead. Cached summaries are tuples (`encodeSummary`). A row merges only
  identical values (text, number, unit), since aggregates read units and spellings. A goal with a
  limit and no comparison or `Total` sets `Conjunction.Page` (an after cursor's values in `Page.After`,
  #254) and solves the returned matches
  through the projector as usual. A Federation pages or summarizes a single-literal goal across
  members, anything for a lone member, and a join across a partitioned federation's members (#253):
  `across` checks every member takes it and pushes `PrefixesOnly`, and `spanning` continues each
  member's prefixes elsewhere (`pushAcross`), adding each crossing match as a summary row of one match
  or a page candidate; such a summary refuses `Extremes`. The corpus's generated goals rarely qualify, so
  `agreeGoals` runs `goalQuestions` per graph on a fresh Base; `pagedPrograms` and
  `summarizedPrograms` are its controls. NaN can't go in a `both()` fixture: `reflect.DeepEqual`
  never equates it with itself.
- **A `WalkSource` follows a relation from value to value** (`walk.go`, `ns/walk.go`, #239).
  `findWalks` runs after `pushConjunctions` and records, per head relation, each looked-up literal
  whose argument at `from` is the recursive call's column j and at `to` the head's column j;
  `prefetchWalks`, called by `applyRule` before `prefetch`, turns a probe of exactly that pattern (from
  plus the literal's constants) into one `Walk` from the values no tier holds. `walk` files each value
  it reaches itself (a BFS from the starts through facts matching the constants) as that value's
  complete lookup, so a stray fact is never a whole answer, and the fixpoint runs unchanged. A
  Federation walks across members, each walking a value once (`walkKey`). A step of several literals
  is walked through `ns.ConjunctionWalkSource` (#250): `stepWalkIn` finds a recursive rule whose
  pushed group reads the recursive column as `in` and writes the head's as `out`; `walkStep` files
  each reached value's rows under `pushKey(g.conjunction(bnd))`, #246's batch key, after unifying
  each row with the group so a non-match is never followed. A demand rewrite that splits the step
  into a supplementary prefix leaves no single group to walk. A walk is found only where the recursive rule
  reads the relation itself, or through a helper `unfold` inlines into it, which takes only a rename
  (one rule, one positive non-predicate literal, no projection: the docs' `deps.depends_on` through
  `_step`, #319); a helper that joins, compares or calls a predicate is probed per round. The corpus's programs
  rarely walk, so `agreeWalked` runs a fixed reach from each graph's first edge over a walking store
  and over the partitioned federation, on a fresh Base (Naive's lookups would leave nothing to walk);
  `walkedPrograms` is its control.
- **`Report.Cold` reruns the query when the Eval was warm** (`coldCost`, #147): it reused a derived
  relation, answered a lookup from an earlier Eval's (`SourceReport.Kept`, #212), or probed a held
  relation a fresh Base would have looked up. The rerun is a copy with no derived or lookup cache, no
  work counter, a fresh `edb` when the Source looks up, and `o.cold` set so it can't
  start another (a cold run that could reuse recursed forever under mutation).
- **A base relation's facts are terms** (`facts.go`, #279): `edge(?a, ?b) as ?r` binds `?r` to
  the fact's `ns.Tuple.ID` (or `TimedTuple.ID`), or `edge("a", "b")` when the store gave none.
  `lowerFacts` runs right after `Link` (in `evaluateOn`, `validateBound`, and per module in
  `resolveAll` after its names resolve), refusing `as` on a negated atom, a predicate or a derived
  relation (`KindFact`), and writes the atom as `\x00f:edge(?a, ?b, ?r)`, so every check and rewrite
  after it sees an ordinary base relation: `schemaIn` gives it `edge`'s schema plus a text `fact`
  column (so `b.schemaOf` and the typer must go through it, never `reg.Schema`), `edbTuples` builds it
  from `edge`'s tuples (`factTuples`, kept as a relation of its own so the id is indexed), and
  `lookup`/`prefetch` ask the store `edge`'s own call (`lookedUpAtom`), so the cache keys are
  `edge`'s. It is never pushed, walked, summarized or streamed (`isFact`), and `withoutOrphans` and
  explain's naming must not take its NUL for a rewrite's relation (the corpus found the first under
  Witnesses). A leaf shows it as `edge` with `Witness.Fact`. `Atom.Fact` is set only by the parser:
  `bindGoal`'s and `Link`'s atom copies must keep it, or `as` vanishes silently. The corpus binds an
  edge's fact now and then from a stream of its own (`generator.facts`, `bound fact`), every third
  generated edge left without an id, and Soufflé reads `edge_fact`. nl's grammar writes `as ?v`
  (`fact`).
- **A rewrite that rebuilds a `Literal` must keep its `at`, and one that rebuilds a `Rule` its
  `text`.** They carry the written position and form a witness follows (`witness.go`); dropping
  either makes that literal vanish from explanations or shows a rule in its rewritten form, with no
  error. `magic.go` and `readingDelta` both rebuild literals.
- **A constant is read as its argument's type (#65).** `coerceConstants` (`coerce.go`) runs on the
  linked program after `bindGoal`, in `evaluate` and `Validate`, so a bound value is checked as a
  constant: text parses into a number argument or is refused, a number in a text or entity argument
  drops its `Num`. It rewrites `Num` only, never `S`, because the Domain check, the index and answer
  keys read the text. Only a number type pulls a compared constant, which keeps #8's "a number and
  a word have no order". `ValidateBound` binds `ns.Absent()`, which coercion and the Domain check
  (`checkArgValues`, #68) both leave alone, so a variable the host will bind is never refused for
  its type or its value. `checkArgValues` is the only validation that reads a constant's value, so
  a new placeholder or substitution has to pass it.
- **A variable bound to several values ranges over a relation of them** (`bindSets`, #132). Bound to
  one it is still a constant (`bindGoal`). A set, or none, stays a variable, and after the checks on
  the written program the goal is joined with `\x00b:<var>`, a relation of facts (for none, a rule
  reading only itself). `planGoal` keeps it first for the demand rewrite, so magic seeds from every
  value; the final `plan` orders the goal freely, since the adorned relations hold only what was
  demanded. Each value is checked as one constant would be (`checkBoundValue`: coerced, and against a
  closed Domain), and stored as a number when any place reads it as one. Its tuples carry no
  citations and no witness node. The planned SemiNaive's first rewrite does the same for a goal's
  equality to text, `?h = "X"` either way round (`bindEqualities`, #370, relations `\x00b:=h`),
  since agents write the constant last, and `factor` reads such a variable as its one value
  (`magician.equals`, `withEqualities`), which a host's several values can't be. A number only
  filters (#227) and is left alone, and so is everything outside the goal.
- **Nothing inside a module resolution may call `Vocabulary.Signature` or `Check`.** They run
  through the memo entry that is mid-computation, and `sync.Once` deadlocks on re-entry. That is
  why the validation base carries `sigs` while it checks module rules.
- **An answer writes a plain number canonically, and data's spelling wins where two meet** (#148).
  `bindArg` keeps, when a bound variable meets an equal number spelled differently, the spelling
  `spelledBefore` ranks first: text saying more than the number (`3.3V`, a range), then a unit, then
  the unit's text (`A` before `V`), then shorter, then smaller text. That makes the choice a minimum, so it doesn't depend on join order.
  `canonicalAnswers` then rewrites a value whose text only spells its number (`01`, `1.50`, `-0`) to
  `ftoa`, in the goal's bindings and witnesses (copying shared nodes), before projection, so dedup,
  order and `list` see one text. Never canonicalize a constant's `S` earlier (#65), and a host-bound
  column is filled in after, as bound. The same rule decides everywhere two equal values meet
  (#201): two derivations of one derived tuple store the better annotation of each value
  (`firstAnnotations` in `addTuple`, whichever derivation `keepCanonical` keeps), and a tuple whose
  annotation changes is listed revised so its consumers read it again, in plain evaluation too;
  an answer row's survivor, a group's key and a distinct value break a value tie the same way
  (`order.survives`, `valueBefore`). Without it, `Witnesses` answered `9 A` where plain answered `9`.
  A query's own number only filters, as in Naive, so it never lends its spelling (#227): `derive`
  writes a magic or factored tuple's numbers plainly with no unit (`plainDemand`), `unfold` doesn't
  inline a call's number into a body that compares or computes with it, and an expression waits for
  every relation binding what it reads (`awaitsBinder`, in `deferComparisons`) before computing.
- **Equality is strict and an equivalence, with one key per value** (`valueEq`, `valueKey`, #162).
  Two numbers compare by value (NaN equals NaN, -0 is 0), two texts by text, and a number never
  equals text. A number's key is `numberPrefix` plus its canonical text, so the index (`tupleKey`)
  files each value under one key and a bucket is exact; `unify` and `valsEqual` still decide.
  Kinds can be strict because both sides are read as the argument's declared type: constants by
  `coerceConstants`, set-bound values by `checkBoundValue` (as text where a place reads text), and
  a Source's tuples by `normalizeTuples` as the Base reads them, whole or through `Lookup`, which
  also writes every plain number canonically, and a generator's emitted rows by its Builtin's
  `Types` (`normalizeValues`). The old rule (number vs text by the number's spelling) made join and
  rule order decide answers. Answers, groups, `distinct` and the Lookup cache key on `valueKey` too,
  so a derived column holding both `1` and `"1"` answers both; only `CanonicalCites`' derivation
  order still reads `keyText`.
- **A variable's name is an ident and a number is decimal** (`checkVarName`, `lexNumber`, #163), as
  the grammar comment in `parse.go` says. `strconv.ParseFloat` alone also takes inf, nan, hex and
  `.5`.
- **The query text is read as tokens first** (`lex.go`, #167), and the parser, the expression parser
  and `parseArgType` read tokens. It keeps the old slicing parser's decomposition, because error order
  and wording come from it: separators are found in the symbols' text (`occurrences`, so `=>` in `!=>`
  counts) and a cut inside a token re-reads that piece (`span.slice`). Every parse error is a
  `*ParseError` with `Line`, `Column` (runes) and `Offset`; `Error()` is the message alone. A string
  takes no escapes, but a backslash keeps the next character in it, so `\"` is a quote inside and both
  stay in the value (#314: Declaire's regexes write `\"`). The old parser is frozen as test code
  (`slicer_test.go`, every change to it marked), and `FuzzParseAgreesWithSlicer` and
  `TestATypoGetsTheSlicersMessage` hold the two to the same `Query` and the same message word for
  word. A grammar change that should make them disagree is when to delete it, keeping its typo
  messages as a golden if they still matter. The differential's allowances (`readsAsWritten`,
  `agreesWithoutExponents`, `unknownAggregate`) cover only the readings #167 changed on purpose;
  match a disagreement by rewriting the text, never by the slicer's wording, which differs by
  where the piece sits (#315 took four tries that way).
- **The parser also reads the endings other Datalogs write, and only forms that never parsed**
  (#276, from Declaire's agent exercise): `.` before whitespace or the end ends a clause
  (the lexer marks that `.`, and `clauses` cuts at it), `?- body` and `=> body` ask `body` with no projection (`goalAfterArrow`), `!atom` is
  `not atom`, and a `.` between a goal and its `=>` is dropped. Each is tested against its jaala
  spelling's `Query`. Uppercase words stay an error with a `?U` or `"U"` hint, on purpose:
  `pin(?r, GND)` read as a variable would match every net. A new accepted form must be one that errs
  today, so no program changes meaning, and `;` stays the spelling the docs and printer use.
- **The answer order is a total order, and hosts see it.** `orderValues` ranks absent, then numbers by
  value, then text (#8). Comparing as numbers only when both are numbers cycles on a mixed column
  (2 < 10, "10" < "1a" < "2"). A row's survivor is the one ordering first among those with its
  `valueKey`s (`order.survives`, as `dedupSort` keeps), so it doesn't depend on arrival, and `1` and
  `"1"` are two rows (#162). An absent value keys apart from `""` (`valueKey`'s `absentKey`, #62), in
  answers and in groups. `order by`, `after`, `limit` and `offset` apply against the columns as
  written, so a host-bound variable is still one (the sink fills it as bound). A `limit` without an
  `order by` is the one exception to the total order: it answers the first rows the goal finds, so
  `offset` and `after` need an `order by` (#194). Moving the default order changes agni's goldens, so
  it ships with an upgrade note.
- **The goal streams into its answer, and never holds its bindings** (`sink.go`, `table.go`, #120).
  A projection keeps one `answerRow` per distinct row (`projector`, reading each binding into a
  scratch row and allocating only one it keeps); an aggregate keeps one `accumulator` per group and
  column (`aggregator`, for a goal and a rule head alike). `sum` is exact (`exactSum`, Shewchuk's), so
  it doesn't depend on arrival, and a NaN makes `min`/`max` NaN wherever it arrives. With an `order
  by` and a `limit`, `pageHeap` holds `limit` plus `offset` rows; without an `order by`, the
  projector returns `errEnough` and the solve stops. `Report.Held` is the peak rows or groups held,
  and `TestTheGoalHoldsItsAnswerNotItsBindings` and the `Memory` benchmark (peak heap, 4 MB where
  `main` held 3.5 GB at a budget of 2,000,000) guard it. A new answer feature reads bindings as they
  arrive; collecting them first brings #120 back. `EvalTable` (on `Evaluator`) returns a `Table`;
  `Eval` converts it, so a host's `[]Row` is built only for returned rows (#195).
- **A solve extends one binding in place and undoes it on backtrack** (`binding`, #260). Each
  extender (`unifyEach`, `extendIDB`, a generator's emit, a filter, `extendPushed`, `streamEDB`)
  takes a `mark`, extends, yields and `reset`s, so a candidate allocates nothing whether or not it
  unifies, and `reset` also restores a spelling `bindArg` replaced (`undo`). What a callback is handed
  is rewritten by the next candidate: anything keeping a binding past the call keeps `bnd.keep()` (the
  projector's `r.bnd` does; `TestACanonicalRowKeepsItsFirstBinding` fails without it), and values read
  out of it are copies already. `vals` is a short slice scanned by `get`, not a map.
- **Comments use agni's circuit vocabulary on purpose** (`doc.go` says so). Host-specific test
  data doesn't belong here, though: checks against agni's real catalog, such as its
  `columnkinds.golden`, live in agni. Copying another repo's catalog in creates a fixture that goes
  stale without failing.

## Testing discipline

- Red-check each new test: break the behaviour it guards, keep the symbol, and confirm the test
  fails on its assertion. When scripting mutations, **treat a build failure as "not checked", not
  as red**. An unused variable left by a mutation fails the build, and a naive harness counts that
  as a pass. Likewise a `-run '^Name$'` that matches no test passes; run by prefix and check the
  test actually ran. A `git bisect run` script that skips (exit 125) on a build failure must match
  `build failed` only: #295's bisect skipped every commit because the query's error text said
  "cannot". Restore mutated files in a `finally` and give each run a timeout. Mutate back
  to the exact old code: one that also loosened a neighbouring check (#89's head `_`) was caught by
  that check under another message, and the saved fuzz input "survived" for the wrong reason. A
  test that loops until a condition (paging until a short page) needs a bound, or a mutation hangs
  it rather than failing it (#194). And a test comparing two APIs one of which is built on the other
  can't fail: `Eval` is `EvalTable` converted, so the table's test checks its content directly.
  `both()` panics on a disagreement, which ends the whole `go test` run, so a mutation checked
  against several tests runs each by its own `-run` (#227: one run reported only the first). A
  planner change that `deferComparisons` redoes at solve time can't be seen by any answer; a
  mutation that reverts it and stays green says so (#227's `planBody` hold, dropped).
- **Give a test a control that proves its fixture can tell the cases apart.** Most surviving
  mutations here were fixtures that could not: a recursion guard tested only with two-rule
  relations, a column-order test whose sort orders coincided, a "free" call the planner bound, a
  citation-leak fixture whose first derivation happened to follow the leaked path, a `ValidateBound`
  test with no closed-Domain argument (#68, which shipped). A `control:`
  assertion (Naive walks n times; the plan does start with the reordered literal) catches that.
  A path a rewrite makes rare can be unreachable in the corpus, so before trusting a surviving
  mutation, check the path runs at all (a temporary print over 1000 seeds). #237's bound-group
  fallback ran only behind a generator the planner puts first, and its first fixture derived every
  tuple in round zero, so emptying the fallback changed no answer; its test checks the bindings the
  fallback passes too.
  A control counting how often the corpus reaches a feature must evaluate on a fresh Base:
  `agreeOver` runs Naive first on the Base it compares, and Naive's lookups fill the Base's cache, so
  the planned run there finds nothing left to walk (#239's control read 0 until it got its own Base).
  A corpus control that counts a rare case has to hold at CI's 300 seeds, not only at the 1000 or
  5000 it was tried at: #192's mixed-units control passed at 1000 and failed CI's default, so it
  applies from 1000 seeds. Run `go test ./datalog` at its default before pushing a new control.
  A control can be satisfied by another path to the same call: #263's count of one-atom pushes read
  above 0 with lone pushes disabled, because a Federation's continuations are one-atom pushes too, so
  it counts only those with no constant or `Inputs`. A path that only a cache hit takes needs a
  fixture asking one key twice: #317's mutation dropping a kept lookup's versions survived until a
  second rule asked the first's `edge("c", ?y)` again. Two Go traps that made mutations survive: the
  constant `-0.0` is plain 0 (use `math.Copysign(0, -1)`), and a peak-heap test streaming many tuples
  measures uncollected garbage under `-race` unless it sets `debug.SetGCPercent(5)` while sampling.
  **Inlining hides rewrites**: a single-rule relation is folded into its caller, so a test of how
  demand, planning or the fixpoint treat a derived relation gives it two rules or runs with
  `Witnesses()` (which turns inlining off). A cost test needs a fixture where the old cost shows:
  a chain stored in walk order closes in one pass (`reversedLine` doesn't), and a reader sees its
  input in round zero only when its name sorts after it.
- **`Naive` is the reference evaluator and stays unoptimized.** `SemiNaive` (semi-naive fixpoint,
  then the rewrites `unfold` → `magic` → `plan`, all off with `WrittenOrder`) must answer as it
  does. The test helpers
  (`eval`, `evalErr`, `evalReg`, `evalRegErr`) route through `both()`, which runs Naive,
  `SemiNaive{WrittenOrder: true}` (same rows and errors) and the planned `SemiNaive{}` (same rows,
  and Naive's error whenever it errors). A tuple reachable two ways keeps whichever derivation an
  evaluator finds first, so plain citations aren't compared; `agree` then runs the query again under
  `CanonicalCites()`, where all three must give the same rows, citations and witnesses (#22). A
  projection with a `limit` and no `order by` may rightly answer different rows per evaluator (#194),
  so `agree` compares its whole answer instead and requires each evaluator's rows to be that many of
  them, distinct, in the default order (`anyRows`, `agreeOnSomeRows`); the Soufflé step checks them
  as a subset the same way. A new evaluator or option belongs in `both()` too. `seminaive_test.go` adds a
  seeded random-graph corpus, and `plan_test.go` a clause-order shuffle property.
- **The generated corpus (`generate_test.go`, #87) runs random programs through `agree`**, the
  comparison `both()` makes, without the panic. Each program runs plain and under `Witnesses()`, and a
  disagreement is shrunk before it's reported (`JAALA_GEN_SEED=n` replays one). It skips under
  `-race` (`raceDetector`, set by `race_test.go`/`norace_test.go`, #143) unless replaying one seed or
  `JAALA_GEN_UNDER_RACE` is set: it evaluates one program at a time and the engine starts no
  goroutines, so the detector found nothing and made it nine times slower (261s against 28s). CI
  runs it in a `corpus` step without `-race`, which fails unless the test passed rather than
  skipped; the concurrent-Base tests still run under `-race`. A replay needs the
  generator to draw in a fixed order, so it never ranges over a map while drawing (`genTypes`, #226:
  `bodyAggregate` did, and a seed's program changed between runs); `TestASeedGeneratesOneProgram`
  generates each seed four times. A disagreement that is
  filed and unfixed goes in `knownDisagreements`, matched by its message, and is counted rather than
  failed. Each entry carries a repro that `TestKnownDisagreementsStillDisagree` runs, so a pattern
  that matches nothing fails at once (an inlined variable's name starts with a NUL, which a terminal
  hides), and so does a fixed bug, whose line then goes. A program is classified by its first
  disagreement, so a known one can mask another: #90 hid about a tenth of the planned comparisons
  until it was fixed.
  `./selfcheck.sh` runs the corpus at 5000 seeds (#86). About 30% of graphs give their weights
  units (#192), half all in volts and half a mix of volts, amps and none (`genUnits`, drawn from a
  source seeded by the graph so a seed's program is unchanged). Soufflé has no units, so
  `corpusCase` compares a mixed seed with its units left off, and a single unit as it is (answers
  compare by text). A unitless graph's programs write arithmetic now and then (#209: `+ - *`,
  negation, `num.add(…)`, in comparisons and as `?y = expr`, sometimes before its binders, never in a
  recursive rule), drawn from a stream of its own (`generator.arith`, from `newGenerator`, which reads
  the units before `corpusCase` drops them) so other seeds' programs don't move. A computed `?y` is
  what a head or the goal reads more often than not: a filter alone hid a wrong `num.add` at 5000
  seeds.
- **Naive is checked against Soufflé** (`souffle_test.go`, #23). The translator is test code, from
  the parsed `Query` and its bound values to a Soufflé program whose relations carry a per-program
  prefix, so a batch of 200 programs is one souffle run (batches run in parallel). Types come from
  unification over every place a variable or constant sits, as Soufflé checks them. An aggregate
  reads a helper relation of the body's bindings (every `_` named, so a binding is a tuple) or of
  the group's distinct values, and gets a rule of its own: Soufflé 2.5 segfaults on a rule whose
  body is two aggregates and nothing else. A variable bound to one value is a constant, as
  `bindGoal` makes it (not a group key), and an aggregate goal with no group column writes jaala's
  row over nothing explicitly. `order by` and `limit` are applied in Go with jaala's own order. A
  program outside the fragment (`list`, a rule head `sum`/`min`/`max` with no group column, a
  division, since Soufflé's numbers are integers) is counted by reason, never dropped. An expression
  is Soufflé arithmetic, every variable it reads refusing `souffleAbsent` (num.* computes nothing
  from absent), and `?y = expr` makes `?y` a column of an aggregate's helper relation. `TestSouffleCatchesAWrongTranslation` drops negation on purpose
  and requires a shrunk disagreement. `make setup` installs Soufflé 2.5, CI's version: Homebrew's
  bottle on macOS (arm64 included), CI's release `.deb` on Ubuntu x86_64. Without it on `PATH`,
  `./selfcheck.sh` prints "could not run".
- **The query text is fuzzed** (`fuzz_test.go`, #89). `FuzzParse`: any text parses or fails with a
  `query:` error, and a parsed rule prints back as text that parses to the same rule. `FuzzEval`: a
  program that parses is validated and evaluated without a panic, every error keeps `query:`, and when
  Naive answers within `fuzzBudget` all three evaluators must `agree`. Plain `go test` (so CI) runs the
  seeds and every input in `testdata/fuzz/`; `./selfcheck.sh` fuzzes each target for
  `JAALA_FUZZ_TIME` (30s), and `frontends/cypher`'s `FuzzCompile` for `JAALA_CYPHER_FUZZ_TIME` (2m):
  #352's second PR took five 10-minute runs to come back clean, four finding a bug 30s never
  reached, so a compiler change gets 10-minute runs until one is clean. A failing input is written to `testdata/fuzz/<Target>/`: fix it, rename the
  file for what it caught, and commit it. A failure is a finding only when the saved input fails
  replayed; otherwise (a worker killed or hung, the machine under load, #169) selfcheck prints the
  run's last lines, removes an input that passes replayed (a killed worker saves the one it was
  running), and fuzzes once more, passing if the rerun does. Never chain a step on `go test ... |
  tail -1 && next`: the `&&` reads tail's status, so a failing test ran the 10-minute fuzz after it.
  Its first hour found a quadratic did-you-mean, a parser panic, `?_: T` not printing back,
  comparison and head-`_` cases the evaluators answered differently, and a `-0` the index missed.
- **A comparison is checked where its body binds it, not where it is written** (#89). Every solve
  runs `deferComparisons` first, which moves a comparison to just after the literal that binds its
  operands, so `?a = "a", edge(?a, ?b)` answers in every evaluator. `checkComparisons` refuses a
  comparison no positive literal binds, on every rule as linked and on the goal, before any rewrite
  can copy it into a rule of its own.
- **Cost is checked against `testdata/work.golden`** (`bench_test.go`, #88): each workload's `Work()`
  under the planned SemiNaive must stay within 10% of its baseline, and its answer keep its row count.
  The corpus checks answers and can't see cost; the baseline is what catches a rewrite that stops
  paying off. Work that drops past 10% fails too, so an improvement lowers the baseline
  (`go test ./datalog -run TestWorkStaysWithinBaseline -update`), and a PR that moves it says why.
  The baseline records today's costs, bad ones included (#96, #97).
- **`Explain(&r)` fills a `Report`** (`explain.go`, #147): the run's `explainer` (on `evalRun`) is
  told which body (`enter`, from `applyRule` and the goal) and literal (`literal`, from `solve` and
  `passesNegationsExplained`) is current, and `step` counts each unit of work against them, so the
  bodies' work adds up to the Eval's exactly (`TestExplainAccountsForEveryUnitOfWork`). Keep the
  hooks off the hot path: `solve` routes through `solveAt` so the yield closure captures nothing
  new, and `applyRule` copies `r` before taking its address. A capture or `&param` there moved a
  value to the heap on every call and doubled points-to's time with Explain off; compare
  `-benchmem` allocations against main, which load doesn't skew. For time, build both test binaries
  (`go test -c`, main from a worktree) and interleave their runs: back to back, load made #151 look
  25% slower where interleaved it was within noise. Rewritten names are made readable by
  `ExplainName`, and `testdata/explain.golden` (`-update`) holds the text. `enter` lists a body's
  literals in `deferComparisons` order, the order `solve` indexes them in (#89).
  An empty answer fills `Report.Empty` (#343) from the counts alone: the first literal that passed
  nothing, its `Missed` (the first call that passed nothing, written in `solve`'s explaining branch
  and `passesNegationsExplained`, never in `solveAt`), and, through a derived relation whose bodies
  derived nothing, each rule's (`explainer.reads` maps a literal to the atom it reads, kept off
  `LiteralReport` so its JSON reads back; a demand guard that passed nothing means the body wasn't
  asked). `LiteralReport.Stopped` is set in `check` when the budget runs out. A count over nothing
  answers a row though its literal passed nothing, which is the control that `Empty` keys on the
  rows: every literal passing something can't tell. Each culprit's atom is then run alone, only its
  constants bound and `limit 1` (`explainer.alone`, after `finish`, #348), on a copy of the Base
  counting no work and keeping no derived relation, as `coldCost` runs, under the Eval's `Budget`
  or `probeBudget`; a base relation the Base doesn't hold (streamed, or only looked up) isn't
  probed unless the Source looks it up and the call has a constant, since that reads the store
  whole (`TestAReadStoppedAtTheBudgetIsNotKept` caught a streamed one read twice). Its test reuses the Base's derived cache so the query is cheap and
  only the probe hits the budget.
- **`CanonicalCites()` keeps each tuple's shortest derivation** (`canonical.go`, #22): fewest rule
  steps (a witness node's `height`), then the rule's written text, then the body nodes in written
  order by relation, values and a leaf's citations. It records witnesses to compare (so `tagWritten`
  runs whenever `witnessing()`; `Row.Witness` is cleared unless `Witnesses()` asked). On a duplicate,
  `addTuple` calls `keepCanonical`, which replaces a later-first derivation, or an equal one whose
  citations or derived children changed, and lists it in `run.revised`; `since` puts revised tuples
  in the next delta and `Naive` loops on them, so a consumer shortens too. Shortest depth is the same
  whatever the order, which is why size-of-citation-set (the issue's first idea) wasn't used: unions
  don't compose, so a local minimum depends on order. It turns off inlining and factoring (as
  Witnesses does) and supplementary relations, which merge derivations differing only in a `_`.
  A row's survivor ties go to the binding `compareBindings` ranks first (`order.survives`).
- **Magic tuples carry no citations.** `magic.go` adds relations recording what a query demanded;
  `SemiNaive`'s `derive` clears their citations, or an answer would cite the facts that worked out
  someone else's demand. Two relations the rewrite adds are not demand and keep theirs on purpose:
  a factored reachable set (`factor.go`), so an answer cites one whole path, and a supplementary
  relation (`\x00s:`, #54), a stored body prefix. Under Witnesses a supplementary tuple carries its
  literals' witnesses as `idbTuple.parts`, which `solve` splices back in at their written positions.
  Factoring is off for a witnessed Eval. A call with nothing bound, from a guarded body, calls the
  all-free relation (`reach_ff`) under a zero-argument magic relation, so a clause whose guard never
  holds derives nothing (#60). From the goal or a rule evaluated in full it reads the original, and
  when the original is read anyway `foldFree` points the all-free calls back at it, then drops the
  rewrite's rules that read what it removed (`withoutOrphans`), since a rule reading a relation with
  no rules errs as unknown rather than deriving nothing.
- **A rule head may aggregate (#4), and that rule is its relation's only one.** `applyAggregate`
  feeds the goal's `aggregator`, so grouping, `distinct`, citations and the one-row-over-nothing case
  match an answer's. `stratify` makes its body edges strict, as negation's are. Demand stops at it
  (`magician.aggregates`): a caller binding the count column names no value of the body, and a
  supplementary relation in its body would make the bindings a count reduces a set. Its
  signature reports the aggregate position as declared when the function fixes the type
  (`aggregateFixes`, #78): `count`, `list`, and `sum`/`min`/`max` over a typed number. Over an
  untyped column those stay `Inferred`. Numeric head constants contribute `number` (#84); a bare
  literal is compatible with the unit other clauses agree on. Text constants and incompatible
  types or units leave the column untyped. Constants alone do not mark a column declared, so an
  undeclared numeric column stays `Inferred`.
- **`min`, `max`, `sum` and `avg` reduce numbers, and with none they answer `absent`, 0 for `sum`**
  (`accumulator`, #122; `avg`, #284, is the exact sum over how many numbers, counting bindings as
  `sum` does, so `countsBindings` and Soufflé's `unsupported("avg")` list it: Soufflé's `mean` is a
  float), over a text column or over nothing, in a goal and in a rule head alike; `count` is 0 and
  `list` the empty text. Never `""`, which reads as a value and keys as one (#62). Soufflé gives no
  tuple for a min over nothing, so its translator writes `souffleAbsent` there and skips, guards and
  reads back that sentinel as jaala treats absent. Over numbers in different units all three are
  `absent` (#80), and otherwise the result keeps the group's unit; a number with no unit takes the
  group's, as in a comparison. `accumulator.add` reads units before `distinct` dedups (5 V and 5 A are
  one number). A query that means something across units says so with `num.value` (strip the unit)
  or `num.unit` (group by it). An absent result from a goal's own aggregate says why in `Row.Why`,
  keyed like `Bind` ("no rows", "no numbers", "mixed units: A, V"); a rule's aggregate stores only its
  value, and a `having`-only column drops its reason with its value. The corpus gives some graphs'
  weights units (#192), and its control requires answers holding a unit, and from 1000 seeds (so
  `./selfcheck.sh`, not CI's 300) an aggregate absent over mixed units, which a unit-blind
  `accumulator` fails; `units_test.go` and the guide's examples
  check the rules themselves. An
  ordering comparison whose two sides have known, different units is refused before running
  (`checkComparisonUnits`, #190), in the goal, rules, `having` and modules (at `Check`): a
  variable's unit is known only when every positive atom binding it declares that unit, since a
  value from a unitless binder compares with any.
- **Arithmetic is syntax over function relations** (`exprs.go`, #5). An `Expr` term, legal only as
  a comparison operand, is `+ - * /`, unary minus, or `name(args)` (a relation with one trailing
  result argument). `lowerExpressions` runs first, with body-aggregate lowering (in `evaluateOn`,
  `ValidateBound` and per module), writing each as calls to stdlib's `num.add/sub/mul/div/neg` on
  fresh `\x00e` variables; `?y = expr` computes into `?y`, so it binds or tests. The calls are
  `computed`: `deferComparisons` (which now takes the vocabulary) holds one until a mode of its
  generator is satisfied, and while something still to run binds a variable it would solve for
  (`awaitsOperand`, #273: num.mul solving `?e * 0 = 0` answers nothing, so a function runs backwards
  only for what nothing else binds; once the body runs out, a call waits only on calls that could run then,
  or two waiting on each other ran in whichever order they were left, `?w = ?x * 2 + 1` erring in Naive;
  `planBody` holds such a call too (#331), since `magic` makes a demand rule from the planned prefix and
  a demand rule has nothing left to wait for: `num.mul(?n, 0, ?p)` planned first demanded no count;
  when every call left waits on another, `planBody` retries waiting only on what `couldRun`, as
  `deferComparisons` does, #374: `r0(0*?0, 0)`'s `num.mul` and `r0` waited on each other, the written
  order put `num.mul` first, and the demand rewrite stored it alone in a prefix nothing bound),
  so written order doesn't matter in Naive, and `checkComputedRecursion`
  refuses a computing rule that reaches itself (#207), on the program as linked only (`checkRules`;
  the fixpoints call `strataOf`), since the demand rewrite's cycles make a computing rule look
  recursive in a program that isn't (#266). `parseOperand` tries `parseTerm` first, so
  a plain operand parses and errs exactly as before; a literal containing `(` is an atom only when
  it has no top-level comparison operator.
- **Units are symbolic, and the engine holds them normalized** (`ns/unit.go`, #5). A `Unit` is a
  scale and symbols with exponents; `Vocabulary.AddUnit` names base units, prefixes
  and derived units (stdlib's `Units` registers SI prefixes and an electrical set), and a symbol
  nobody named is its own unit. A query literal (`3.3V`, `1kohm`; a bare `e` suffix stays #163's
  error) is folded by `foldUnits` in `coerceConstants`, in rule heads too (#224), and a module's
  rules are folded in `resolveAll` before their signatures are inferred: the scale goes into the number and the unit
  is written by its first name (`A*ohm` is V, at most one name factored out of a composite:
  `ohm/s`). `num.*` write results the same way, so every value inside the engine is in base units
  and unit strings still compare as strings. A unit is a tag to the engine; what it means is the
  vocabulary's (#303). A Source's values are folded as they are read only when the vocabulary opts
  in (`Vocabulary.FoldSourceUnits`, #205; `b.units()` is nil otherwise, on every path a value comes
  in by), so `1000 mV` is then `1 V` everywhere, and otherwise a Source's `mm` stays `mm` (agni
  read its bare thresholds that way, and v0.1.59's default fold changed its answers). A host-bound
  set is a query's constant and always folds. stdlib's units are SI products (mass in kg), written
  through `WriteBaseThrough` so composites keep their names. A scale is a power of ten times an
  exact factor (`normFactor`'s one form, so `60*s` and `6e1*s` are one unit), so a host names
  `min` as `60*s`. `Conjunction.Units` and `Walk.Units` are set only when the vocabulary folds,
  and a Federation folds what it carries between members by them: the corpus's 5000 seeds (not
  CI's 300) found a partitioned join meeting `0.004 kV` unfolded against `4 V`. The corpus and the
  prefixed tests run over `folding(src)`; the `looking` store's `asStored` answers an engine that
  doesn't fold.
- **A rule can take an argument from its caller** (`inputs.go`, #200): a head variable its body
  can't bind, which a comparison, a `not` or a generator reads, is an input (`inputsOf`; a call to an
  input relation binds only its other arguments, so `inputRelations` grows to a fixpoint). What a
  body binds is `bindsFrom`, the fixpoint `checkModes` computes (a generator binds once a mode is
  satisfied), so `power(?v, ?i, ?p) :- ?p = ?v * ?i` is a function an expression calls as
  `power(?v, ?i)` (#208). Where the body binds head variables only from each other, the head's
  earliest unbound ones are the inputs, which only names them in the signature: a call is accepted
  whenever `bindsFrom` over the inlined body binds them, and `inline` keeps `computed`. Only a
  relation with one rule, a head of distinct variables, no aggregate and no recursion qualifies;
  anything else keeps today's `head variable ?x is not bound` error. The written-program checks
  treat inputs as bound (`inputsBound`, `b.inputRels`), then `lowerInputs` inlines each call into
  its caller just before the evaluator reads the rules, so all three evaluators see ordinary rules,
  and refuses a call that doesn't bind an input, a negated call, and a counting caller of a
  projecting input relation (#139's multiplicity). Validate and module resolution (at `Check`) run
  the same lowering for its errors. `ArgSig.Input` marks the argument.
- **An aggregate in a body is lowered to rules before anything else runs** (`bodyagg.go`, #71).
  `?v = f(?x) : { body }` becomes a domain relation of the shared variables (the clause's positive
  relations that don't read an aggregate's value; no citations, no witness), a head aggregate over
  the braces (no witness), and a value relation with a zero clause from `emptyAggregate` (#122). It
  runs first in `evaluateOn` and `ValidateBound`, and in `resolveAll` per module, so every check and
  both evaluators see ordinary rules; Naive and SemiNaive can't disagree about it, and only the
  Soufflé step checks the lowering, which it writes in Soufflé's own form (a min or max as two
  bodies). Helper names start with the aggregate as written and `aggSep`, so `displayName`, a
  witness node and `whereRule` show `count(?n)` or the literal. A variable written only inside the
  braces is theirs alone.
- **Demand goes through negation and into it (#34).** If the rewritten program doesn't stratify
  (a recursive caller negating what it demands), `magic` redoes it with negated calls reading their
  relations in full. That usually stratifies, but the corpus found two cases where it doesn't (#93):
  demand flowing from a rule above an aggregate down into the relation it reduces, and a negation
  still in a cycle the demand closes. When neither rewrite stratifies, `magic` returns the program
  unrewritten, which `checkRules` has already stratified. A relation the rewrite would derive under two
  adornments (`severalAdornments`, #96: the copies can cover it twice, which made demand for one
  points-to variable cost three times the whole analysis) is read in full and the rest rewritten
  again (#290); dropping the whole rewrite instead let one relation's callers decide every other
  relation's plan. A relation whose every rule is only generators runnable from its constants
  (`constantRun`, #290) is never demanded either: demand runs each generator once per caller value
  (Declaire's `sends` over `graph.reach`: 86,460 walks for 7,205 callees, against 12 in full). A
  rule reading a base relation keeps demand, since in full it is scanned rather than probed. A call
  that only a generator called with nothing bound would bind isn't demanded per value (#291:
  Declaire's `sent/ff`, whose `loc.home` demanded all 136,181 functions), because the planner runs
  the derived call first (#293), so it binds nothing; #291's own check (`fromWide`) went with that,
  as no plan reached it. A rule evaluated in full also has its
  constant calls rewritten (`fromConstants`, #57), adorned by the constants alone, so their demand
  rules are facts and add no dependency. A call its earlier literals bind is not demanded there, since
  a full rule binds it for every row (doing so cost `series-from-a-net` 170 times its baseline),
  except in a rule `unfold` would have inlined into the goal had the Eval not been witnessed
  (`asGoal`, `sideways`, #270): Witnesses turn inlining off, and without this a witnessed goal over
  such a rule derived what it calls in full (Declaire's q09r). The corpus reaches `sideways` but
  never its bound path; `TestAWitnessedRuleStandingInForTheGoalPassesItsBindingsOn` guards it.
- **A Base keeps the derived relations SemiNaive evaluated in full** (`derived.go`, #140). The key is
  the sorted text of the relation's linked rules and those of every derived relation they reach
  (`derivedKeys`; text, not a hash, for the deps check above), so a redefinition anywhere below is
  a new key. `reuseDerived`, SemiNaive's first
  rewrite, drops a held relation's rules and installs its tuples, so demand reads it by index rather
  than deriving part of it; after the fixpoint, a keyed relation still derived under its own name is
  kept (a rewrite renames whatever it derives in part, and nothing enforces that for a new one).
  Off for Naive, `WrittenOrder`, and any witnessed Eval, which includes `CanonicalCites()`. A relation reaching a `Volatile` predicate has no key. `ns.Versioned` and
  `Base.Forget` drop both caches; an Eval stores only into the generation it started in, and
  `edbCache.get` files no index over tuples read before a reset. Work tests that reuse a Base across
  queries see the second one cheaper: give each its own Base, or `LimitDerivedCache(0)`. A probe
  that misses derives and keeps what it read, which can evict what the next probe checks, so a test
  checks its expected misses last. A host may keep them past the Base (#220): `KeepDerivedIn` adds
  an `ns.DerivedStore` tier behind the memory one, keyed by `ns.DerivedKey{Relation, Rules,
  Version}` and used only over a `Versioned` Source; `SemiNaive.Materialize` fills it ahead of a
  query. A failed store read derives; the store is never called under the cache lock.
- **SemiNaive drops the rules the goal never reaches before rewriting** (`withoutUnreached`, #90).
  A rewrite renames or removes what they read, which left them reading a relation with no rules.
  They are checked first, as linked, so their mistakes are still reported.
- **Every call's arity, and every goal name, is checked on the program as written**
  (`checkWrittenArity`, #133, #127), before any rewrite: the demand rewrite reads a call's arguments by
  its relation's arity and panicked on a short call, and solving only checks an atom it reaches, so an
  unknown goal name after an atom matching nothing went unreported. `TestBrokenProgramsFailTheSameWay`
  breaks the generated programs (an argument dropped or added, a relation renamed) and requires every
  evaluator to refuse each with the same `query:` error, never a panic; `selfcheck.sh` runs it too.
- **A negation's anchor is checked on the program as written** (`checkWrittenAnchors`, #92): the goal
  before `Bind` turns the host's variables into constants, with those variables counted as anchors,
  and the rules before a rewrite inlines or renames them. Checks that run later (`applyRule`, the
  rewritten goal) use `checkNegatedRelations`, which checks relations and arity only.
- **Rules are checked as linked before any rewrite renames them** (`checkRules` in `evaluate`), so
  an error names `r`, never `r\x00/bf` or a factored relation. A new rewrite gets this for free;
  a new check that names a relation belongs there too.
- **Inlining must not change multiplicity.** `unfold.go` inlines single-rule, non-recursive derived
  relations, but never a projection (a body variable, `_` included, the head drops; `projects`,
  #139): probing one inlined `has_tp(GND)` per caller re-lists every test point on GND, where demand
  derives each net once. Nor into a goal or rule head whose aggregate counts bindings (`count`, `sum`,
  `list` without `distinct`): a derived relation is a set, its inlined body is not. Nor into a recursive relation's
  rules (#97): the fixpoint reruns that body every round, so an inlined join is redone each time. A
  rename (`renames`: one rule whose body is one positive call to a relation, not a predicate) is
  inlined there anyway (#319), since it adds no join and the body then reads the walked relation. Modes are checked on the linked
  program before any rewrite, so inlining a rule away can't hide an unrunnable body.
- **Generators declare `Modes`; `checkModes` is shared validation, the planner is SemiNaive's.** A
  body that can never satisfy a generator is refused by every evaluator and by `Validate` with one
  message, checked on the linked program before any rewrite. Planning lives in `plan.go`, called
  only by `SemiNaive`. A generator runs as soon as one of its modes is satisfied with an argument
  bound, after only the ready checks (comparisons, filters, and relations with every argument
  bound), so a host never has to write a body generator-first (#36). One called with nothing bound
  lists every value, so it ranks after a relation the engine derives or holds (`readsWhole`, #293:
  Declaire's `loc.home` listed 136,181 homes ahead of `http.pattern`'s 100), and still ahead of one the
  Source looks up or the goal may stream, whose whole read goes to the store (the prefetch tests'
  `step(?h, ?x), attr(?x, ?v)`). A variable only its all-free mode would take counts as unbound
  (`generatorBound`, #304: agni's `net.reaches(?n, ?rn, ?h)` with only `?rn` bound walked the board
  once per TVS pin); a constant still counts, since it binds the call once. A generator also waits in `deferComparisons`, as an expression does,
  for a relation still to run that binds a variable it reads where its `Types` say number
  (`numbersRead`, #244), so it reads the spelling they agree on; one typing nothing, as most hosts'
  do, is never held. Naive doesn't plan: it calls a generator where it is
  written, mode or not, with the unbound inputs `Bound: false`, so a generator must refuse what it
  can't run without (`str.distance` errors like a filter, #77) rather than read the zero value; a
  `both()` test of a generator-first body catches it as Naive answering differently. A filter is
  refused the same way when nothing in its body binds an argument (`checkFilters`, #215, after
  `checkRules` so a redefinition reports first), and `deferComparisons` holds one until its arguments
  are bound, so Naive answers a filter written before its binder. Relations tied on bound arguments (at least one) go by
  `fanOut`, the base relation's tuples per call from its index (#139): `part(?r, "capacitor")`
  scans every capacitor, `pin(?r, ?a)` with `?a` bound reads one net. It reads the Source at plan
  time, and a failed read is reported after the rewrite (`evalRun.readErr`). A body the demand rewrite guarded (a magic,
  supplementary or factored relation, `isGuard`) keeps the guard first when `plan` runs over it (`planRule`); ranked
  from nothing bound, the guard would fall behind any relation bound by constants.
- **SemiNaive derives a stratum component by component** (`components`, Tarjan, in dependency
  order). A stratum is a level, so it mixes recursion with plain dependencies; a relation that
  doesn't read itself, even through others, is derived once, and delta rounds run only inside a
  recursive component (#51). `stratify`'s strata, its errors, and `Naive` are unchanged.
  Round zero runs the rules in turn, so each rule's round-one delta starts at a mark taken just
  before it ran (`peekMark`, #149): it has already joined what came before. `peekMark` keeps the
  `CanonicalCites` revised lists (only `marks` starts them over), so a tuple revised after the rule
  ran is still in its delta; `TestRoundOneRereadsWhatRoundZeroRevisedAfterARuleRan` guards that,
  since the corpus doesn't reach it.
- **A name added to stdlib moves more than stdlib's tests** (#360). The plain-language card lists every
  member, so `frontends/nl`'s `card.golden` and `eval.golden` (`-update`) and the docsite's
  `Example_card` (`examples/guide/plainlang_test.go`, edited by hand) change, as do the two datalog
  tests listing `str`'s members (`checkerror_test.go`, `namespace_test.go`). `TestEveryBuiltinHasADemo`
  wants a `demos/reference/builtins-*.yaml` calling it, and a new error message the catalogue. Each line
  is prompt cost for every `nl.Translate` call (#360's six took `TestEval` from 10,401 tokens to
  14,190), so keep a builtin's `Doc` short. A text function is a relation with its result last, like
  `num.*`, so expressions take it with no engine change; an absent input gives no row. When a test
  marks a value unbound by comparing with `ns.Value{}`, `ns.S("")` is that value too.
- Fixtures: `workloads`, `binaryTree`, `pointerProgram` and `netlistOf` (synthetic, agni-sized) in
  `bench_test.go`; `graph()` and the `eval`/`evalErr`/`col`/`std`/`baseFor` helpers in `helpers_test.go`;
  `withModules`/`evalReg` in `module_test.go`; the agni-shaped `circuit()` in `signature_test.go`;
  `vocabulary()` (no Source) in `baseover_test.go`; the `stub` language in `ns/vocabulary_test.go`;
  `line`/`walker` (a two-mode generator recording what each call had bound) and `tested()` (line
  with tests as attributes, Declaire's shape) in `plan_test.go`; `hopper` (a generator citing its
  path in walk order) and `workOf` in `magic_test.go`; `reversedLine` and `counter` in
  `seminaive_test.go`; `answer`/`fresh` (one query's own work), `versioned` and `stepper` (a
  generator counting its calls, optionally `Volatile`) in `derived_test.go`; `probedBoard` (caps between GND and their own nets, test points on both) in
  `scaling_test.go`; `parts()` (counts and numbers whose text and value orders differ) in
  `order_test.go`; `typedNets()` (number counts, a numeric-looking ref, a pin stored as `ns.N`, an
  untyped relation) and `answersAs` in `coerce_test.go`; `netlist()` (C1's two pins both on GND, so
  counting bindings and distinct values disagree; `ohms` carries a unit) in `aggregate_test.go`; `params()` (a part's parameters in V, A and ohm, a
  bare number, and 5 V beside 5 A) in `units_test.go`; `digits()` (n(1) to n(10), so a three-way join finds a thousand bindings),
  `reported` (the planned SemiNaive with Explain) and `peakHeapDuring` in `paging_test.go`; `store` (a
  repo's facts behind a LookupSource with an index catalog, counting calls and scans; a 60-long
  `next` chain for `deep-walk`), `batchingStore`, `repoStores`, `newStoreSet` (N of them, batching,
  federated by id prefix) and `unbatchedStoreSet` in `stores_test.go`, mission #219's exercise;
  `looking` (a LookupSource, optionally sloppy or failing), `batching` (it with `LookupBatch`,
  recording each batch) and `fanned(n)` (a hub calling n nodes with attributes) in `lookup_test.go`;
  `versionedLooking` in `lookupcache_test.go`. `wide` (an `edge` relation made as it's read, counting what it made) and `streamingWide` in
  `stream_test.go`, with `streaming` (any Source streamed, counting what it `handed` by relation),
`chain()` (40 edges, enough to index), `lookingStreams` and `lookingUpLabels`; a `store` streams
unless `sliced`, and counts what it `shipped`. `pushing` (a
  looking store that evaluates conjunctions by its own join, `evalConjunction`, optionally sloppy,
  refusing comparisons, or failing; `forBindings` and `withInputs` read a conjunction's `Bindings`) in
  `push_test.go`, and the `store` pushes too, answering `Bindings` through its index; `joinMem`
  (optionally `noPartial`, `noInputs`, `sloppy`, recording what it was `pushed`) and
  `partitionedEdges` (#238's two-store example) in `ns/federation_test.go`. The corpus's federations
  leave their members unnamed: a `Name` prefixes citations, which `agreeOver` compares. `both()` takes Eval options, so a `Bind`
  case runs through all three evaluators. `both()` compares rows in order, so every test checks the
  answer order too. Don't compare rows by `fmt.Sprint`: `ns.Value.Num` is a pointer, so the text
  carries an address. datalog's tests import `stdlib` for `std()`; production datalog code must not.

## Releasing

Merge the PR, then start the CI set and `./selfcheck.sh` on the merged `main` (PRs that merged
together were never tested together). The owner picks the version; so far releases are patch bumps
on v0.1.x, breaking changes included, pre-1.0, and a tag needn't wait for the checks: when they
fail, fix it and tag the next patch. agni consumes tags only (`go get github.com/panyam/jaala@vX`),
never a `replace`.

Release with `make tag-push V=v0.1.N NOTES=notes.txt` on the merge commit (`scripts/`, after
mcpkit's). It tags jaala and every module in the Makefile's `SUB_MODS_TO_TAG`
(`frontends/nl`, tagged `frontends/nl/v0.1.N`, then `nlmodels`, `mcp` and `cypher`) at the same commit, pushes jaala's tag
alone and then the others (GitHub raises no push event for several tags in one push, mcpkit #1391),
and publishes the release page from jaala's annotation, which is the release notes: a short list
of what changed, from NOTES. `make tag` alone tags without pushing.

Every sub-module keeps a `replace` of jaala, and of each tagged module it requires (`cypher` and
`nlmodels` require `frontends/nl`, `mcp` requires `cypher` and through it nl), by the checkout, so
its CI and tests build against `main`, and its requires name the last release. Before tagging, `make
bump-root V=v0.1.N` (mcpkit's model: `scripts/bump-root.sh`) points those requires, and the
docsite's, at the version about to be tagged with `go mod edit`, adds any missing replace, and
tidies through the replaces, so V needn't exist yet; commit it to `main`, then `make tag-push` on
that commit. A consumer ignores `replace` and reads the requires, which resolve once the tags are
pushed. `tag-all` refuses a sub-module requiring any of jaala's modules at a version other than V
(bump-root forgotten). With V tagged already it tags only the sub-modules, at HEAD, for a sub-module
tag that failed, and `tag-push` then pushes only theirs. After pushing, check each resolves:
`GOPROXY=direct go list -m github.com/panyam/jaala/<dir>@V` from a scratch module. v0.1.72 to
v0.1.75 went out in tiers, from a `bump-root` that dropped the replaces and tidied against
published tags; that is why they took several commits. Put the upgrade note on the issue a
host filed or the PR's own issue.

## Issues and missions

Issues are ranked by the mission they serve (labels `P0`–`P3`, `waiting`, `mission`,
`mission:active`, `mission_<slug>`), and its tickets are linked as blocked-by. Most of jaala's
missions unblock a host's active mission; such a mission closes when the host's half of its exercise
passes too, so it can stay open after jaala's tickets close. Two are active. The first is
`mission_agent_frontend` (#322), a host's caller asks in plain language and jaala's front end
turns it into a program that checks. jaala's half is `frontends/nl` and `frontends/nlmodels`
(#321, #323 to #326, #330, #331, #336, #337, released in v0.1.66 and frontends/nlmodels/v0.1.66);
`go test -run TestEval -v .` in `frontends/nl` is its jaala-side exercise (fix line 5/5). Declaire's
translator-alone arm (declaire#295, for declaire#290) first ran on v0.1.66 and passed 2 of 14 on
Qwen2.5-Coder 7B against declaire#288's bar of 10; its jaala asks became #340 and #341 (PR #342,
released in v0.1.67). Declaire's 32B run then stopped on an unexplained empty answer on 6 of its
11 failures (declaire#296), which became #343 and #348 (PRs #346 and #349, released in v0.1.68):
`Report.Empty` names the culprit and whether it matches alone, and `Translate` sends that back.
#344 (domain packs, `nl.Pack` and `nl.DataCard`, PR #350, v0.1.69) and #347 (`frontends/mcp`, an
MCP server for any vocabulary that a host wraps with its pack, PR #351, v0.1.70 and the first
`frontends/mcp/v0.1.70`) followed; Declaire moves its translator and server onto them in
declaire#302, and its wording experiments (declaire#308) replace `mcp.Instructions`' default. The mission's done-when points at
declaire#288's translator line rather than quoting its number, which is Declaire's to set.
The second active mission is `mission_cypher_frontend` (#356, from epic #278), started 2026-10-18:
an agent or the model translating for it writes Cypher, and `frontends/cypher` compiles it into a
program that checks, so Declaire can measure whether Cypher or jaala's syntax costs an agent less per
correct answer (declaire#288's "one arm per front end"; the arms are declaire#248). Its tickets: #352
(the compiler over a graph view of a vocabulary, and `go test -run TestEval -v .` in `frontends/cypher`,
`nl`'s cases in Cypher; PRs #359 and #369), #353 (errors located in the Cypher text, #372), #354
(`nl.Translate` writing Cypher, the 32B arm) and #355 (`frontends/mcp` taking Cypher, closed by
#366's `cypher` tool and `cypher.Store`, PR #368). #279 (facts as terms, #371) came out of
`waiting` beside them; the other gaps (#280 to #284) and #364 (`CALL { }`, an order or limit in a
stage) stay `waiting` until an exercise case is refused for one. Next is #354. The two store missions closed on 2026-10-16. `mission_store_federation` (#219), a host answers a question over many stores without holding them
in memory, measured by the `Stores` benchmark (#218: stores asked, unindexed probes, round trips,
peak heap and a repeated question's fetches as the number of stores grows) and Declaire's `bench
-time` on a federated view. The benchmark's stores run in its process, so `peak-MB` counts their
work too: `page`'s 15 MB at 50 stores is mostly the fixture's own paging (`pageOf` builds every
match before sorting), not the engine, so profile before blaming jaala. `peak-MB` also moves run
to run (`attrs-of-reached` read 14.3 to 17.2 MB over four runs of one commit), so rerun with
`-count 3` before reading a few MB as a regression; store calls and `shipped` are exact. Run it as
`go test ./datalog -run '^$' -bench 'BenchmarkStores/.*/stores=50' -benchtime 1x`. Its seven jaala tickets closed, the last #233 (a `StreamSource` read
charged to the budget, from declaire#211's measurement).
`mission_store_pushdown` (#235), epic #213's second half: a store answers the part of a question it
can evaluate (a conjunction, a walk, an aggregate, a page) and jaala combines the results, measured
by the same benchmark's `shipped` column and its questions `two-hops-in-repo`, `count-lines`, `page`,
`self-loops` (#236) and `attrs-of-reached` (q09r's shape, #268); `edges` is the unbound read no store
filters, and `edges-budgeted` carries #233's stopped read. All fourteen of its jaala tickets are
closed (#236 to #241, #246, #250, #252 to #254 by v0.1.50; #263 and #268, from Declaire's first
measurement and folding #259, by v0.1.52; #270, a witnessed goal's bindings reaching what its rule
calls, q09r's `http.route`, after v0.1.52): a conjunction, a walk, a count, a page, a batch of
bindings (across a partitioned federation too), a walked step and a lone filtered literal each go to
a store, and every jaala line of its "Done when" holds. Declaire's first real-view timing (x04 in
its `bench/external`, declaire#255: 2m44s on stores against 4.6s on mem) reopened it: #290
(v0.1.55) read generator-only relations in full, taking x04 to 51.5s; #291 (v0.1.56) stopped a push
at a filter, 14.6s; #293 (v0.1.57) ranked an all-free generator after held relations, 3.6s, under
mem's 3.3s. #297 (v0.1.57) lets a store take `str.glob` with a push (`ns.FilterSource`), which x05
needs Declaire to implement. All 18 jaala tickets closed. The earlier four are closed too: `mission_selfcheck` (#86, at v0.1.29) left `./selfcheck.sh` as the check to
run on `main` after every merge and before every tag; `mission_docsite` (#104) left the docs site
with every example run by the engine; `mission_aggregate_answers` (#172, at v0.1.42) left Soufflé
checking every aggregate program but `list`; and `mission_browser_memory` (#193, closed with agni
#896) left the `Memory` benchmark's peak heap in use (not `B/op`, which the join's short-lived
bindings dominate) as the measure of what a query holds. Several missions can be active at once (`queue.sh` prints them). A new issue gets a
priority and a mission link when filed, or `waiting` with the trigger that would unpark it.

Create the branch before the first commit (`git branch --show-current` says so). #200's work was
committed on local `main`, so `git reset --soft main` to squash it did nothing and the PR opened
with WIP commits. Squash against `origin/main`, never a local name that may have moved.

Another session may tag a release while your PR is open: v0.1.65 shipped `nl` at the root while
#337, which moved it, was being planned on "no tag ships it yet". Check `git fetch --tags` and
`git tag --contains` before arguing that a path or API change is free.

Stacked PRs (each branched off the one below, its base that branch) review and merge in order, and
GitHub retargets the next to `main` when its base merges; confirm each merge commit is an ancestor of
`origin/main` before deleting branches. Merged first, the upper PR lands in the lower's branch, not
`main` (#275 did): its issue stays open until the lower PR merges, and is closed by hand then. Run each branch's `./selfcheck.sh` in a detached worktree
(`git worktree add --detach <dir> <branch>`) so the checkout stays free for the next branch, and
remove the worktree after. A selfcheck run on the working tree while editing it tests half-written
code, and so does one whose tree gets a scratch test dropped in (a before/after capture belongs in
another worktree). Run it in the background as `./selfcheck.sh > log 2>&1` with nothing after it: a trailing
`; echo exit $?` made the task report 0 for a selfcheck that failed (#209's, which found #273).
When several branches are in flight (parallel agents), run their selfchecks one at a time, and
timings only on an otherwise idle machine; never `pkill -f selfcheck.sh`, which kills every run on
the machine (#220's agent did). A branch rebased onto a moved `main` gets its selfcheck again, and
so does `main` when two PRs branched from one base merge together (#305 and #306 did). Another
session may be running one too: wait on it (`while pgrep -f "/bin/sh ./selfcheck.sh"; do sleep 20;
done` before your own, in the same background command) rather than starting alongside.

Pushing a branch can get a PR opened for it under the owner's account (#168 was), titled and
bodied from the commit. Check `gh pr list --head <branch>` before `gh pr create`, and edit that PR
rather than opening a second one. A PR from an outside contributor gets the owner's review before
it merges, however green its CI: such PRs tend to settle a ticket's open questions on their own
(#184). When `main` moves under one, merge `main` into its branch (maintainers may push to it)
rather than rebasing, so the contributor's commit stays as written, and say so on the PR with what
the conflict resolution kept. Deleting an open PR's head branch closes it unmerged (#187 was closed that way), so check
`gh pr view N --json state` says `MERGED` before cleaning up a branch.

A PR body closes only its own issue. GitHub reads any "fix", "fixes", "close", "closes" or "resolves"
before `#N` as a closing keyword, even inside a phrase like "Engine fixes: #90", which closed #90 on
merge (reopened). In "Out of scope" and "Related" write "left for #N".

## Working with hosts

A field added to a struct a host implements against (`ns.Conjunction`) is ignored silently by every
host built before it, so behaviour that depends on the host acting on it is opt-in through a new
optional interface (`ns.FilterSource`, #297): Declaire's store accepted filters it didn't apply, and
groups grown past them joined every row the filter would drop.

Most of what a host derives while ingesting stays in Go however much jaala grows (a survey of both on
2026-10-18): parsing, type resolution (go/types, SCIP), minting identities, and anything ordered.
Declaire's linkers are rules waiting on nothing in jaala since v0.1.71 (declaire#205), and moving them
retires its hand-built linker store. agni's net roles and part classes could be rules too, but some 90
Go rules, guards, trace and rendering read them off `check.Model`, so a rule there duplicates the Go
until agni#1061 settles the model's shape. Write statements (#287) add edits; they replace no ingestion.

agni (github.com/panyam/agni) is the first host and Declaire (github.com/panyam/declaire) the
second. Cross-repo work is split by repo: jaala issues are worked from jaala sessions, host issues
from host sessions. A host that needs something jaala lacks files a jaala issue rather than working
around it, and a release that breaks hosts gets an upgrade note on the issue they filed, with the
exact lines each must change (as #7's v0.1.6 comment did for `Modes`).

To see what a change does to a host before writing its upgrade note, run the host's own suite
against `main` and against the branch through a scratch `go.work` (`use` the host checkout and a
jaala worktree), and diff the failures: a failure in both is the host's or its environment's, one in
the branch only is this change's (#161, #168). Diff the whole sorted list of failing test names
(`grep '^--- FAIL' | sed 's/ (.*//' | sort -u`), never a `head` of it: a truncated list reported 9
of agni's 13 environmental failures for #196. A host checkout pins an older tag, so a copy may need
a small patch to build against `main` (agni's `Bind` call, for #132); patch a copy in the scratchpad,
never the host's own checkout. With no local checkout, `gh repo clone panyam/agni -- --depth 1`
into the scratchpad. Run the suite before believing what a doc says a host does: #205 assumed agni
normalizes units upstream (its C24), but agni serves board geometry in `mm` and reads bare
thresholds in it, so folding changed two of its answers (upgrade note on agni#1035). agni moved to
metres (agni#1036) before #303 made the fold opt-in, so a host's own migration can outrun jaala's
fix: check its `go.mod` and recent PRs before writing it an upgrade note.

Reproduce a host's ask before building it: Declaire's run asked for "a Fix for a qualified rule
head", which v0.1.65 already had; the real gap (#341) was the error after the model followed it,
since the unknown-name fix never read the program's own rules. Feeding the model's likely next
draft through `Validate` and `nl.Explain` in a scratch module shows which message it actually gets.

To find why a host's question is slow, run it alone: Declaire's `bench -dir <dir> -engine stores
-time` over a directory holding its `workspace.txtpb` and only that question (#290 did this for x04).
Declaire's `Runner` passes `Explain` only when its context carries a `*datalog.Report`, so reading
which relations were demanded and where the time went needs a patched copy of both repos in the
scratchpad, joined by a `go.work`; the Explain text (`demand [b], 7205 demanded`) then names the
rewrite to blame. Run the whole `bench/external` before and after a planner or push change, not only
the question it targets: #297's first cut sped nothing up and tripled x04's fetched rows, which only
the counts showed. Read `fetched`, `work` and `pushes` rather than times while anything else runs
(a background selfcheck took v0.1.56's x04 from 14.6s to 39s); they are exact. A question failing on
both engines and on older tags can still be jaala's: x06b was #295's deadlock, fixed by v0.1.56.
