# The engine

Constraints on `ns`, `stdlib` and `datalog`: errors, shared state, values, parsing, answers, facts and engine walks.

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
  Witnesses). A leaf shows it as `edge` with `Witness.Fact`. A bound fact anchors a negation
  (`positiveVars` reads `Atom.Fact`, #441: the written-program checks run before `lowerFacts` makes
  it an argument). `Atom.Fact` is set only by the parser:
  `bindGoal`'s and `Link`'s atom copies must keep it, or `as` vanishes silently. The corpus binds an
  edge's fact now and then from a stream of its own (`generator.facts`, `bound fact`), every third
  generated edge left without an id, and Soufflé reads `edge_fact`. nl's grammar writes `as ?v`
  (`fact`).
- **`graph.closure("rel", ?from, ?to, min, max, ?hops)` is the engine's** (`closure.go`, #281): a
  breadth-first walk of a binary relation over (node, depth capped at min) states, `?hops` the fewest
  steps, max 0 none, min 0 a node itself only when a fact holds it (`onStep`, #280: otherwise a bound
  end, as demand binds it, answered what a walk from every start didn't). `lowerClosures` (before `Link`, and per module before `resolveRule`, whose
  `renameAtom` renames the step inside the name and the first argument) writes it as `\x00c:rel`
  with its six arguments, so everything that classifies by name reads it: `predicateIn` (never call
  `reg.Predicate` directly in production datalog, except for a step or a head) makes it a generator of
  three modes, `relationsRead` makes `Link`, `reached`, `recursiveRelations` and `derivedKeys` see
  the step, and `stratify` puts the step strictly below (`depEdge.walk`). The planner counts only its
  ends as bound (`generatorBound`), or an all-free closure ran first (x10b, 46,536 work against 254).
  It reads the step through `extendAtom`, so Naive and SemiNaive share it, and the planned evaluator
  walks a step whose rules each read one base relation through those atoms (`walkSteps`, off when
  witnessed), or a `cypher_step` would be derived in full. Under `CanonicalCites` the path is the
  least shortest one walking out (`leastPath`), so either end gives one witness; `Across` refuses it.
  `ns` reserves the name. Over a store, `prefetchClosures` (`closurefetch.go`, #396, from `prefetch`,
  so the planned SemiNaive only) walks ahead of a closure or paths call from every start its prefix
  binds: one `Walk` with `MaxDepth` the call's max when the store walks its one step read
  (`walkedStep`), else a `LookupBatch` per level (`prefetchLevels`), stopping at the bound ends when
  every start has one and min is at most 1. `stepReads` is the one place a step's atoms are built,
  so the prefetch's keys are the walk's. `walk` files a value only below `MaxDepth`, and a Federation
  routes a crossing value with its steps left. The planner doesn't run an all-free walk over a looked-up
  step first (`walksStore`): that read the store whole ahead of the literal binding its start.
  The corpus draws closures from a stream of its own (`generator.closures`), and now and then walks a
  one-atom rule over the relation instead (`generator.steps`, shape "derived step", #438: an unbound
  walk's starts read the renamed step `\x00w:` itself, which only `stepReads` serves),
  Soufflé reads them as a depth-counting walk and a `min` (`souffleClosures`), and `agreeAcross`
  skips them. `stdlib.Hops` is the same walk over a host's step function, `?within` an input.
- **`graph.paths("rel", ?from, ?to, mode, min, max, ?path)` is the engine's too** (`paths.go`, #280),
  and shares the closure's plumbing through `engineWalk` (`walkOf`, `isWalk`, `walkName`; a new
  engine walk is one more entry in `engineWalks`). It binds each path as an `ns.Path`'s text, a JSON
  list of nodes or, over a relation of three arguments (from, edge, to), `{"nodes", "edges"}`, which
  stdlib's `path.*` read with no state. Modes walk (needs a max), trail (no fact twice; with edges,
  no edge either way, `trailKey`), acyclic, simple (acyclic, but may close back on its start, and
  goes no further once it has), and shortest (breadth first, each node's steps sorted by
  where they lead, so the path doesn't depend on fact order, and walked out from each start even when
  `?to` is bound, so every plan picks the same one). `stepsFrom` reads a node's steps once per call,
  dedups a fact served twice, and the walk counts a unit per step it tries. A `path.*` function gives
  no row for a value that isn't a path, never an error: the 3000-seed corpus found the demand rewrite
  handing `path.length` a caller's `"v8"` that Naive never passed. Likewise a mode or bound that comes from data walks
  nothing (a constant one is refused at check): FuzzEval found Naive erring on a closure's max of
  `"v0"` in a rule the planned evaluator dropped. Cypher refuses `relationships(p)` over a type the
  vocabulary derives, whose relationships have no fact ids (FuzzCompile). `stdlib.PathValues` registers
  them under `path` (in `Register`), so a host's own module named `path` resolves a bare `edge` to
  `path.edge`; jaala's fixtures' module became `hop`. `ColumnKinds` lowers the walks first, so a
  closure's or a path's ends are typed by the step there too (#281's weren't). The corpus draws paths
  from `generator.walks`, only where the closure stream left a literal alone, so its closures stay
  the seeds' own; Soufflé counts them unsupported. `stdlib.Paths` and `EdgePaths` are the same over
  a host's step.
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
  stay in the value (#314: Declaire's regexes write `\"`). So query text can't spell a value holding a bare quote: code looking a host's value up binds it
  (`datalog.Bind`) rather than writing it into text, and an extraction (`readDraft`) reads a draft's text values
  unescaped, as it reads the `from` words (#444: a quoted key never found the host's fact). The old parser is frozen as test code
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
