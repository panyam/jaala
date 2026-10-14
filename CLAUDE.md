# jaala

A Datalog engine for graph-shaped data, extracted from agni. Three packages: `ns/`, the contract
(the namespace tree, value and signature types, members, suggestions, the `Language` hook);
`stdlib/`, the standard vocabulary (`str.*`, `num.*`, `absent`, the glob and regex compilers); and
`datalog/`, the engine (parsing, linking, `Check`'s resolution and inference, evaluation). Read
`ns/doc.go`, then `datalog/doc.go`. jaala is a Datalog engine, not a graph library: a host's fast
path is a generator (its own Go running inside a query), not a jaala function called around the
engine (#41).

`docsite/` is the documentation site (#104), an s3gen site in its own Go module so its dependencies
never reach jaala's. `docsite/README.md` says how to add a page and an example; every example runs on the engine at build
time and in `docsite`'s tests (`demo.Run`, #106), so an engine change that changes an answer the docs
pin fails the docs workflow. `make -C docsite check` builds it and runs its tests, and `.github/workflows/docs.yml` runs that on every PR and deploys `main` to Pages.

## Commands

`./selfcheck.sh` runs the host-free checks at full size: the generated corpus at 5000 seeds, the
work baselines, fuzzing, and the same 5000 seeds against Soufflé when `souffle` is on PATH (it
prints "could not run" otherwise). CI (`.github/workflows/ci.yml`) runs the set below, and a
separate `souffle` job installs Soufflé 2.5's release `.deb` and compares 1000 seeds; all must pass:

```sh
gofmt -l .                      # must print nothing
go vet ./...
go test -race -count=1 ./...
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
- **A relation a `StreamSource` hands over is charged to the budget as it arrives** (#233,
  `readTuples`). Each tuple calls `evalRun.readOne`, which counts `run.read` beside `run.used`
  against the budget, so a budgeted query stops having held at most that many tuples. A read isn't
  work: `Base.Work`, `Report.Work` and `Explain` leave it out, so the work baselines and
  `TestExplainAccountsForEveryUnitOfWork` don't move, and only `BudgetExceeded.Work` includes it. A
  stopped read fails and isn't kept. A Federation always streams (its members in turn, a slice
  member's tuples one by one). A lookup's results still arrive as a slice (#242).
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
- **A Source may say which lookups it answers from an index** (`ns.IndexedLookups`, #154).
  `planBody` ranks a looked-up call on a pattern the Source says is unindexed as if nothing were
  bound (`scansOnLookup`), so an indexed call runs first; nothing else reads it, so a Source held in
  memory plans as before and the work baselines don't move. A Federation's pattern is indexed only
  when every member serving it is. `agreeFederated`'s stores index only the first argument, so the
  corpus runs the reordering; `TestStoresExercise`'s `callees-with-attr` is its acceptance case.
- **A `PushSource` evaluates a conjunction of a body's base literals** (`push.go`, `ns/push.go`, #237).
  `pushConjunctions` runs after `plan`, in the planned SemiNaive only, and replaces each group the
  Source's `Pushes` accepts with one literal over `\x00p:{…}` whose arguments are the group's
  variables (`run.pushed` holds the group). A group starts at a pushable literal and takes each later
  literal sharing a variable with it and none with a literal outside it before its place; variables
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
  `Holds` allows for the first atom with `Partial` (every prefix of its matches too), and continues each
  prefix in every other member `Holds` allows for the next atom (`Conjunction.after` substitutes the
  prefix's values), so a match is found once and a loose `Holds` costs calls, never answers. The
  engine sees only whole rows. The corpus runs every program over `pushing` (`agreePushed`,
  alternating a store that refuses comparisons) and over a partitioned three-member federation dealt
  by first argument (`agreePartitioned`); `pushedPrograms` and `partitionedPrograms` are its controls.
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
  Federation walks across members, each walking a value once (`walkKey`). The corpus's programs
  rarely walk, so `agreeWalked` runs a fixed reach from each graph's first edge over a walking store
  and over the partitioned federation, on a fresh Base (Naive's lookups would leave nothing to walk);
  `walkedPrograms` is its control.
- **`Report.Cold` reruns the query when the Eval was warm** (`coldCost`, #147): it reused a derived
  relation, answered a lookup from an earlier Eval's (`SourceReport.Kept`, #212), or probed a held
  relation a fresh Base would have looked up. The rerun is a copy with no derived or lookup cache, no
  work counter, a fresh `edb` when the Source looks up, and `o.cold` set so it can't
  start another (a cold run that could reuse recursed forever under mutation).
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
  citations and no witness node.
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
- **A variable's name is an ident and a number is decimal** (`checkVarName`, `isDecimal`, #163), as
  the grammar comment in `parse.go` says. `strconv.ParseFloat` alone also takes inf, nan, hex and
  `.5`. The parser slices rather than lexes; a tokenizer is filed separately.
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
- **Comments use agni's circuit vocabulary on purpose** (`doc.go` says so). Host-specific test
  data doesn't belong here, though: checks against agni's real catalog, such as its
  `columnkinds.golden`, live in agni. Copying another repo's catalog in creates a fixture that goes
  stale without failing.

## Testing discipline

- Red-check each new test: break the behaviour it guards, keep the symbol, and confirm the test
  fails on its assertion. When scripting mutations, **treat a build failure as "not checked", not
  as red**. An unused variable left by a mutation fails the build, and a naive harness counts that
  as a pass. Likewise a `-run '^Name$'` that matches no test passes; run by prefix and check the
  test actually ran. Restore mutated files in a `finally` and give each run a timeout. Mutate back
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
  disagreement is shrunk before it's reported (`JAALA_GEN_SEED=n` replays one). A replay needs the
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
  compare by text).
- **Naive is checked against Soufflé** (`souffle_test.go`, #23). The translator is test code, from
  the parsed `Query` and its bound values to a Soufflé program whose relations carry a per-program
  prefix, so a batch of 200 programs is one souffle run (batches run in parallel). Types come from
  unification over every place a variable or constant sits, as Soufflé checks them. An aggregate
  reads a helper relation of the body's bindings (every `_` named, so a binding is a tuple) or of
  the group's distinct values, and gets a rule of its own: Soufflé 2.5 segfaults on a rule whose
  body is two aggregates and nothing else. A variable bound to one value is a constant, as
  `bindGoal` makes it (not a group key), and an aggregate goal with no group column writes jaala's
  row over nothing explicitly. `order by` and `limit` are applied in Go with jaala's own order. A
  program outside the fragment (`list`, a rule head `sum`/`min`/`max` with no group column) is
  counted by reason, never dropped. `TestSouffleCatchesAWrongTranslation` drops negation on purpose
  and requires a shrunk disagreement. Locally Soufflé is built from source on arm64 (no package); on
  x86_64, extract CI's release `.deb` (`dpkg-deb -x`) into a scratch directory and put its
  `usr/bin` on `PATH` for `./selfcheck.sh`, which otherwise prints "could not run".
- **The query text is fuzzed** (`fuzz_test.go`, #89). `FuzzParse`: any text parses or fails with a
  `query:` error, and a parsed rule prints back as text that parses to the same rule. `FuzzEval`: a
  program that parses is validated and evaluated without a panic, every error keeps `query:`, and when
  Naive answers within `fuzzBudget` all three evaluators must `agree`. Plain `go test` (so CI) runs the
  seeds and every input in `testdata/fuzz/`; `./selfcheck.sh` fuzzes each target for
  `JAALA_FUZZ_TIME` (30s). A failing input is written to `testdata/fuzz/<Target>/`: fix it, rename the
  file for what it caught, and commit it. A failure is a finding only when the saved input fails
  replayed; otherwise (a worker killed or hung, the machine under load, #169) selfcheck prints the
  run's last lines, removes an input that passes replayed (a killed worker saves the one it was
  running), and fuzzes once more, passing if the rerun does.
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
- **`min`, `max` and `sum` reduce numbers, and with none they answer `absent` and 0** (`accumulator`,
  #122), over a text column or over nothing, in a goal and in a rule head alike; `count` is 0 and
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
  generator is satisfied, so written order doesn't matter in Naive, and `checkComputedRecursion`
  refuses a computing rule that reaches itself (#207). `parseOperand` tries `parseTerm` first, so
  a plain operand parses and errs exactly as before; a literal containing `(` is an atom only when
  it has no top-level comparison operator.
- **Units are symbolic, and the engine holds them normalized** (`ns/unit.go`, #5). A `Unit` is a
  power-of-ten scale and symbols with exponents; `Vocabulary.AddUnit` names base units, prefixes
  and derived units (stdlib's `Units` registers SI prefixes and an electrical set), and a symbol
  nobody named is its own unit. A query literal (`3.3V`, `1kohm`; a bare `e` suffix stays #163's
  error) is folded by `foldUnits` in `coerceConstants`, in rule heads too (#224), and a module's
  rules are folded in `resolveAll` before their signatures are inferred: the scale goes into the number and the unit
  is written by its first name (`A*ohm` is V, at most one name factored out of a composite:
  `ohm/s`). `num.*` write results the same way, so every value inside the engine is in base units
  and unit strings still compare as strings. Source values are still expected in base units
  (prefixed ones are #205).
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
  unrewritten, which `checkRules` has already stratified. It also returns it unrewritten when the rewrite would derive one
  relation under two adornments (`severalAdornments`, #96): the copies can cover the whole relation
  twice, which made demand for one points-to variable cost three times the whole analysis. A rule evaluated in full also has its
  constant calls rewritten (`fromConstants`, #57), adorned by the constants alone, so their demand
  rules are facts and add no dependency.
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
  checks its expected misses last.
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
  rules (#97): the fixpoint reruns that body every round, so an inlined join is redone each time. Modes are checked on the linked
  program before any rewrite, so inlining a rule away can't hide an unrunnable body.
- **Generators declare `Modes`; `checkModes` is shared validation, the planner is SemiNaive's.** A
  body that can never satisfy a generator is refused by every evaluator and by `Validate` with one
  message, checked on the linked program before any rewrite. Planning lives in `plan.go`, called
  only by `SemiNaive`. A generator runs as soon as one of its modes is satisfied, after only the
  ready checks (comparisons, filters, and relations with every argument bound), so a host never
  has to write a body generator-first (#36). Naive doesn't plan: it calls a generator where it is
  written, mode or not, with the unbound inputs `Bound: false`, so a generator must refuse what it
  can't run without (`str.distance` errors like a filter, #77) rather than read the zero value; a
  `both()` test of a generator-first body catches it as Naive answering differently. Relations tied on bound arguments (at least one) go by
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
  `stream_test.go`; a `store` streams unless `sliced`, and counts what it `shipped`. `pushing` (a
  looking store that evaluates conjunctions by its own join, `evalConjunction`, optionally sloppy,
  refusing comparisons, or failing) in `push_test.go`, and the `store` pushes too; `joinMem` and
  `partitionedEdges` (#238's two-store example) in `ns/federation_test.go`. The corpus's federations
  leave their members unnamed: a `Name` prefixes citations, which `agreeOver` compares. `both()` takes Eval options, so a `Bind`
  case runs through all three evaluators. `both()` compares rows in order, so every test checks the
  answer order too. Don't compare rows by `fmt.Sprint`: `ns.Value.Num` is a pointer, so the text
  carries an address. datalog's tests import `stdlib` for `std()`; production datalog code must not.

## Releasing

Merge the PR, then run the CI set and `./selfcheck.sh` on the merged `main` (PRs that merged
together were never tested together), then put an annotated tag on the merge commit and push it
(`git tag -a v0.1.N <merge-sha>`, `git push origin v0.1.N`). The owner picks the version. So far
releases are patch bumps on v0.1.x, breaking changes included, pre-1.0. agni consumes tags only
(`go get github.com/panyam/jaala@vX`), never a `replace`. The tag's annotation is the release
notes: write it as a short list of what changed, then publish the page with
`gh release create v0.1.N --title v0.1.N --notes-from-tag --latest` (from v0.1.32; earlier tags
have no page), and put the upgrade note on the issue a host filed or the PR's own issue.

## Issues and missions

Issues are ranked by the mission they serve (labels `P0`–`P3`, `waiting`, `mission`,
`mission:active`, `mission_<slug>`), and its tickets are linked as blocked-by. Most of jaala's
missions unblock a host's active mission; such a mission closes when the host's half of its exercise
passes too, so it can stay open after jaala's tickets close. Two are active:
`mission_store_federation` (#219), a host answers a question over many stores without holding them
in memory, measured by the `Stores` benchmark (#218: stores asked, unindexed probes, round trips,
peak heap and a repeated question's fetches as the number of stores grows) and Declaire's `bench
-time` on a federated view. Its seven jaala tickets are closed, the last #233 (a `StreamSource` read
charged to the budget, from declaire#211's measurement); it stays open for Declaire's half.
`mission_store_pushdown` (#235), epic #213's second half: a store answers the part of a question it
can evaluate (a conjunction, a walk, an aggregate, a page) and jaala combines the results, measured
by the same benchmark's `shipped` column and its questions `two-hops-in-repo`, `count-lines`, `page`
and `self-loops` (#236). #237 (`ns.PushSource`, v0.1.46), #238 (`NewPartitionedFederation`,
v0.1.47) and #239 (`ns.WalkSource`, v0.1.48) are closed, so two hops within a repo is one call and
`deep-walk` one call at any store count; #240 (aggregates), #241 (pages), #246 (a group bound partway
through a body, batched) and #250 (a walk whose step is several literals) remain. jaala's
earlier four are closed: `mission_selfcheck` (#86, at v0.1.29) left `./selfcheck.sh` as the check to
run on `main` after every merge and before every tag; `mission_docsite` (#104) left the docs site
with every example run by the engine; `mission_aggregate_answers` (#172, at v0.1.42) left Soufflé
checking every aggregate program but `list`; and `mission_browser_memory` (#193, closed with agni
#896) left the `Memory` benchmark's peak heap in use (not `B/op`, which the join's short-lived
bindings dominate) as the measure of what a query holds. Several missions can be active at once (`queue.sh` prints them). A new issue gets a
priority and a mission link when filed, or `waiting` with the trigger that would unpark it.

Create the branch before the first commit (`git branch --show-current` says so). #200's work was
committed on local `main`, so `git reset --soft main` to squash it did nothing and the PR opened
with WIP commits. Squash against `origin/main`, never a local name that may have moved.

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
never the host's own checkout.
