# jaala

A Datalog engine for graph-shaped data, extracted from agni. Three packages: `ns/`, the contract
(the namespace tree, value and signature types, members, suggestions, the `Language` hook);
`stdlib/`, the standard vocabulary (`str.*`, `absent`, the glob and regex compilers); and
`datalog/`, the engine (parsing, linking, `Check`'s resolution and inference, evaluation). Read
`ns/doc.go`, then `datalog/doc.go`. jaala is a Datalog engine, not a graph library: a host's fast
path is a generator (its own Go running inside a query), not a jaala function called around the
engine (#41).

`docsite/` is the documentation site (#104), an s3gen site in its own Go module so its dependencies
never reach jaala's. `docsite/README.md` says how to add a page and an example; every example runs on the engine at build
time and in `docsite`'s tests (`demo.Run`, #106), so an engine change that changes an answer the docs
pin fails the docs workflow. `make -C docsite check` builds it and runs its tests, and `.github/workflows/docs.yml` runs that on every PR and deploys `main` to Pages.

## Commands

`./selfcheck.sh` runs the host-free checks at full size: the generated corpus at 5000 seeds and the
work baselines (Soufflé when #23 lands). CI (`.github/workflows/ci.yml`) runs exactly these,
and all must pass:

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
  fragments of them. New cases get new wording; existing wording doesn't move.
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
  #126), unless the Base already holds the relation whole or is `Unindexed`. What it returns lives on
  `evalRun.looked`, never the Base, so a probed relation never accumulates there. Anything that sizes
  a base relation (`fanOut`, `scanSize`) checks `looksUp` first and reports "unknown" rather than
  reading it. The corpus runs every program over a `looking` wrapper too (`agreeLookingUp`).
- **`Report.Cold` reruns the query when the Eval was warm** (`coldCost`, #147): it reused a derived
  relation, or probed a held relation a fresh Base would have looked up. The rerun is a copy with no
  derived cache, no work counter, a fresh `edb` when the Source looks up, and `o.cold` set so it can't
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
- **The answer order is a total order, and hosts see it.** `orderValues` ranks absent, then numbers by
  value, then text (#8). Comparing as numbers only when both are numbers cycles on a mixed column
  (2 < 10, "10" < "1a" < "2"). `dedupSort` sorts before it dedups, because dedup keys on text
  (`N(1)` and `S("1")` are one row) and the survivor must not depend on arrival. An absent value
  keys apart from `""` (`keyText`, the index's `absentKey`, #62), in answers and in groups. `order by`, `limit`
  and `offset` apply last, against the columns as written, so a host-bound variable is still one.
  Moving the default order changes agni's goldens, so it ships with an upgrade note.
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
  that check under another message, and the saved fuzz input "survived" for the wrong reason.
- **Give a test a control that proves its fixture can tell the cases apart.** Most surviving
  mutations here were fixtures that could not: a recursion guard tested only with two-rule
  relations, a column-order test whose sort orders coincided, a "free" call the planner bound, a
  citation-leak fixture whose first derivation happened to follow the leaked path, a `ValidateBound`
  test with no closed-Domain argument (#68, which shipped). A `control:`
  assertion (Naive walks n times; the plan does start with the reordered literal) catches that.
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
  `CanonicalCites()`, where all three must give the same rows, citations and witnesses (#22). A new evaluator or option belongs in `both()` too. `seminaive_test.go` adds a
  seeded random-graph corpus, and `plan_test.go` a clause-order shuffle property.
- **The generated corpus (`generate_test.go`, #87) runs random programs through `agree`**, the
  comparison `both()` makes, without the panic. Each program runs plain and under `Witnesses()`, and a
  disagreement is shrunk before it's reported (`JAALA_GEN_SEED=n` replays one). A disagreement that is
  filed and unfixed goes in `knownDisagreements`, matched by its message, and is counted rather than
  failed. Each entry carries a repro that `TestKnownDisagreementsStillDisagree` runs, so a pattern
  that matches nothing fails at once (an inlined variable's name starts with a NUL, which a terminal
  hides), and so does a fixed bug, whose line then goes. A program is classified by its first
  disagreement, so a known one can mask another: #90 hid about a tenth of the planned comparisons
  until it was fixed.
  `./selfcheck.sh` runs the corpus at 5000 seeds (#86).
- **The query text is fuzzed** (`fuzz_test.go`, #89). `FuzzParse`: any text parses or fails with a
  `query:` error, and a parsed rule prints back as text that parses to the same rule. `FuzzEval`: a
  program that parses is validated and evaluated without a panic, every error keeps `query:`, and when
  Naive answers within `fuzzBudget` all three evaluators must `agree`. Plain `go test` (so CI) runs the
  seeds and every input in `testdata/fuzz/`; `./selfcheck.sh` fuzzes each target for
  `JAALA_FUZZ_TIME` (30s). A failing input is written to `testdata/fuzz/<Target>/`: fix it, rename the
  file for what it caught, and commit it. #164 (two facts spelling one number differently) is
  counted, not failed, through `spelledApart`, with its repro pinned in
  `TestTwoFactsSpellingOneNumberAreAKnownDisagreement`; #162 (text and numbers comparing
  non-transitively) isn't, and can fail a fuzz run. Its first hour found a quadratic did-you-mean, a parser panic, `?_: T` not printing back,
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
  Answer rows sort their bindings by `compareBindings` before `dedupSort` keeps the first.
- **A value the demand rewrite copied from the query gives way to data's spelling** (#148). A goal
  constant seeds the relations the rewrite adds (`isGuard`), so a planned SemiNaive could answer `3.3`
  where Naive, reading the fact, answers agni's `3.3V`. `binding.weak` marks a variable bound from
  such a value (and `idbTuple.weak`, a bit per position, carries it through a derived tuple); the first
  numerically equal value read from data replaces it. Only a number is marked (equal text is the same
  text), and clones share the map, so `markWeak` replaces it rather than writing to it: marking every
  value and copying the map per clone cost a demand workload 6% more allocations.
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
  reuses the goal's `aggregate`, so grouping, `distinct`, citations and the one-row-over-nothing case
  match an answer's. `stratify` makes its body edges strict, as negation's are. Demand stops at it
  (`magician.aggregates`): a caller binding the count column names no value of the body, and a
  supplementary relation in its body would make the bindings a count reduces a set. Its
  signature reports the aggregate position as declared when the function fixes the type
  (`aggregateFixes`, #78): `count`, `list`, and `sum`/`min`/`max` over a typed number. Over an
  untyped column those stay `Inferred`. A constant in any head leaves its column untyped (#84), so
  a default clause like `r(?n, 0)` needs the type declared on another clause (`?c: number`).
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
  counting bindings and distinct values disagree; `ohms` carries a unit) in `aggregate_test.go`. `both()` takes Eval options, so a `Bind`
  case runs through all three evaluators. `both()` compares rows in order, so every test checks the
  answer order too. Don't compare rows by `fmt.Sprint`: `ns.Value.Num` is a pointer, so the text
  carries an address. datalog's tests import `stdlib` for `std()`; production datalog code must not.

## Releasing

Merge the PR, then run the CI set and `./selfcheck.sh` on the merged `main` (PRs that merged
together were never tested together), then put an annotated tag on the merge commit and push it
(`git tag -a v0.1.N <merge-sha>`, `git push origin v0.1.N`). The owner picks the version. So far
releases are patch bumps on v0.1.x, breaking changes included, pre-1.0. agni consumes tags only
(`go get github.com/panyam/jaala@vX`), never a `replace`.

## Issues and missions

Issues are ranked by the mission they serve (labels `P0`–`P3`, `waiting`, `mission`,
`mission:active`, `mission_<slug>`), and its tickets are linked as blocked-by. Most of jaala's
missions unblock a host's active mission; such a mission closes when the host's half of its exercise
passes too, so it can stay open after jaala's tickets close. `mission_selfcheck` (#86) is jaala's
own: `./selfcheck.sh` checks the evaluators against generated programs and Soufflé, and cost against
`Work()` baselines. `mission_docsite` (#104) is the documentation site, every example on it run by
the engine. Several missions can be active at once (`queue.sh` prints them). A new issue gets a
priority and a mission link when filed, or `waiting` with the trigger that would unpark it.

A PR body closes only its own issue. GitHub reads any "fix", "fixes", "close", "closes" or "resolves"
before `#N` as a closing keyword, even inside a phrase like "Engine fixes: #90", which closed #90 on
merge (reopened). In "Out of scope" and "Related" write "left for #N".

## Working with hosts

agni (github.com/panyam/agni) is the first host and Declaire (github.com/panyam/declaire) the
second. Cross-repo work is split by repo: jaala issues are worked from jaala sessions, host issues
from host sessions. A host that needs something jaala lacks files a jaala issue rather than working
around it, and a release that breaks hosts gets an upgrade note on the issue they filed, with the
exact lines each must change (as #7's v0.1.6 comment did for `Modes`).
