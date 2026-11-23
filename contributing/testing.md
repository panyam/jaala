# Testing

Running the checks, and how jaala's tests are written and red-checked.

## Running the checks

`./selfcheck.sh` runs the host-free checks at full size: the generated corpus at 5000 seeds, the
work baselines, fuzzing, the same 5000 seeds against Soufflé when `souffle` is on PATH, and a real
`llama-server` under nl's grammar when `make llama` has fetched it (each prints "could not run"
otherwise). The corpus step takes about 5.5 minutes alone and runs under `-timeout 30m`, as the
Soufflé step does (#381: under a load near 28 it passed go test's default 10 and failed). Near 80
even 30 wasn't enough: 36f59f2's note says FAIL with only `FAIL ... 1800s` to show for it, since
selfcheck's filter drops the timeout's goroutine dump and the next step overwrites the output. Rerun
that step's `go test` line alone with its output kept before bisecting; there it passed in 18m at a
load near 50. `make setup` installs both.
`make selfcheck` runs it on a commit (`REF`, default `origin/main`; `SEEDS`, `FUZZ`, `CYPHER_FUZZ` for a
bigger run) in a worktree of its own, in the foreground with its log teed to `~/.cache/jaala/selfcheck/`,
and records the result as a git note on that commit (`refs/notes/selfcheck`, pushed unless `NOPUSH`).
`make selfcheck-status` reads the notes: the last pass on `main`, a failure since with the `git bisect
start` that brackets it, and whether a run is due, which is once five PRs have merged since the last
pass (`EVERY`). CI checks every PR; selfcheck runs on that cadence rather than per PR. Every Makefile recipe is a one-line call into `scripts/`
(after mcpkit's): put a new target's shell there, never in the recipe. CI (`.github/workflows/ci.yml`) runs the set below, and a
separate `souffle` job installs Soufflé 2.5's release `.deb` and compares 1000 seeds; all must pass.
Its `scan` job runs `make lint seccheck secrets` (staticcheck, gosec, gitleaks over the history, #459,
after agni's), over the root and every module in `SUB_MODS_TO_TAG` through `scripts/scan.sh`, each
tool pinned in the Makefile and run by `go run`. A gosec finding is fixed or annotated `// #nosec
G<n> -- <why>` on its line (the corpus's seeded `math/rand` is, in `corpus.stream`), and a frozen file
takes `//lint:ignore` rather than an edit. Every tool runs under each module's own `go` directive (setup-go
sets `GOTOOLCHAIN=local` from v6, and staticcheck then refused `frontends/mcp`'s 1.26.9); under an
older Go gosec loaded nothing there and still said 0 issues. A module whose run failed fetching from
the proxy is retried once. `make vulncheck` runs
weekly and on each tag (`vulncheck.yml`, which files a P1 issue on failure), not per PR; `make audit`
runs all four. `SECURITY.md` is the reporting policy:

```sh
gofmt -l .                      # must print nothing
go vet ./...
go test -race -count=1 ./...
go test -count=1 ./datalog -run '^TestGeneratedProgramsAgree$'   # the corpus skips under -race (#143)
GOOS=js GOARCH=wasm go build ./...
go list -deps ./... | grep '\.' | grep -v '^github.com/panyam/jaala' | grep -v '^vendor/'   # must print nothing
```

CI only builds for wasm. To run a package's tests there (Node on PATH), go's runner refuses a large
environment ("total length of command line and environment variables exceeds limit"), so start it
from an empty one: `env -i HOME=$HOME GOCACHE=$(go env GOCACHE) GOMODCACHE=$(go env GOMODCACHE)
PATH="$(go env GOROOT)/lib/wasm:$(go env GOROOT)/bin:$(dirname $(which node)):/usr/bin:/bin" GOOS=js
GOARCH=wasm go test ./ns` (#417 ran `ns` that way).

## Discipline

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
- **Measure a ticket's proposed control on `main` before building on it.** #392's control, the same
  walk asked once per id with the key bound, cost 465,530 work against the `IN` form's 53,580: it
  shared the bug (a plan tie, not the compiler), and testing against it would have passed a half fix.
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
  comparison `both()` makes, without the panic. `internal/corpus` draws each seed's graph and program
  and shrinks a case (`corpus.Shrink`, `Break`), and `generate_test.go` compares, so a store module
  (#411's `stores/bolt`) runs the same seeds against its own Source rather than a copy of the
  generator; it writes the engine's closure and paths names itself, which
  `TestTheCorpusSpellsTheEnginesNames` holds to datalog's. Each program runs plain and under `Witnesses()`, and a
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
  recursive rule), drawn from a stream of its own (`generator.arith`, from `corpus.New`, which reads
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
- **Cost is checked against `testdata/work.golden`** (`bench_test.go`, #88): each workload's `Work()`
  under the planned SemiNaive must stay within 10% of its baseline, and its answer keep its row count.
  The corpus checks answers and can't see cost; the baseline is what catches a rewrite that stops
  paying off. Work that drops past 10% fails too, so an improvement lowers the baseline
  (`go test ./datalog -run TestWorkStaysWithinBaseline -update`), and a PR that moves it says why.
  The baseline records today's costs, bad ones included (#96, #97).

## Fixtures

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
