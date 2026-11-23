# Evaluation and rewrites

How `datalog` checks, rewrites and evaluates a program: Explain, citations, aggregates, arithmetic, units, lists, optionals, demand, the derived cache, inlining and planning.

- **`?k = constant` binds `?k` when no positive literal does** (`constants.go`, #457), with a constant
  goal column (`=> ?x, "tag"`) rewritten to one by `constantColumns`. `lowerConstants` runs before
  `lowerExpressions` in Eval, Validate and a module's rules: in a rule the constant is written into
  the head and body, and in the goal it is merged into a copy of the host's bind, as `bindGoal`
  fills a column. A host-bound or aggregated variable keeps its equality as a test. A selected
  constant groups an aggregate (`evalOptions.grouped`, `aggregator.grouped`): over nothing it answers
  no row, where a host's `Bind` answers one (`rowsAcross` reads it too: the 5000 seeds found
  Across giving the row); Soufflé caught that difference. nl's grammar takes a text or number column. `order by` a constant
  keeps its old refusal, and the slicer differential allows the new column (#457). The corpus draws
  such equalities from `generator.consts`, never under an aggregate (which the engine refuses).
- **A comparison is checked where its body binds it, not where it is written** (#89). Every solve
  runs `deferComparisons` first, which moves a comparison to just after the literal that binds its
  operands, so `?a = "a", edge(?a, ?b)` answers in every evaluator. `checkComparisons` refuses a
  comparison no positive literal binds, on every rule as linked and on the goal, before any rewrite
  can copy it into a rule of its own.
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
- **A list is a value carried as its canonical JSON text** (`ns/list.go`, #283), as a path is: `ns.ListText`
  writes it (a number in a unit as `{"num": 3.3, "unit": "V"}`, so it reads back as that number;
  `ns.Path` shares the encoding), `ns.ParseList` reads it, and `ns.TypeList` (`?l: list`) marks a column,
  read as text by coercion. `list(?x)` returns one (`[]` over nothing, `""` kept as a value), its
  values sorted by `orderValues` then unit then spelling: equal numbers in two units tie in
  `orderValues`, and with the unit written the tie showed arrival order (the corpus's paging store
  found it). stdlib's `list.member`, `list.length` and `list.at` (`ListValues`, in `Register`) read
  it, a non-list giving no row, and a list written in query text, whose `\"` the parser keeps, read
  unescaped (`readList`). A rule can't build one other than through the aggregate (#1's stage 1, so
  termination holds). The corpus collects edges into a list and reads it back from the goal
  (`generator.lists`, `listRead`). Cypher's `collect`, `UNWIND`, `x IN l` (a value, property or
  `$param` bound to a list's text), `size`, `head`, `last`, `l[i]` (`listIndex`) and list literals
  of constants (`listConst`) compile to it; what's left names #405.
- **`optional { body }` is lowered to rules before body aggregates are** (`optional.go`, #282),
  in every place they are (`evaluateOn`, `validateBound`, `ColumnKinds`, per module): `dom(S)` from
  the clause's positive literals not reading what the braces bind, `has(S)` (from `dom` only when
  the braces can't bind S themselves, or a recursive clause's optional recursed through its
  negation), `opt(S, N)` once per match and `opt(S, absent…)` when `not has(S)`. N is everything the
  braces bind, read elsewhere or not, so the clause keeps a row per match as a join does. Names start
  `optional` and `aggSep`, so module checks and `whereRule` treat them as a body aggregate's. Absent
  keeps jaala's meaning (equal to absent, no order, skipped by `count(distinct)`, `sum`, `min`, `max`,
  `list`); a front end with other null rules writes them, as Cypher's does. The typer lets an absent
  head constant abstain, or every variable an optional binds lost its type. Soufflé counts an optional
  unsupported (its only absent sentinel is a number); `TestAnOptionalIsALeftOuterJoin` holds it to a
  join written in Go instead. The corpus wraps a graph atom now and then (`generator.optionals`); it
  found the recursive clause, an expression's variable the rewrite missed, and a pushed group shared
  by two rules at different written places (`push.go` names a group by its literals' positions
  when witnessing). #370's `bindEqualities` leaves alone text that reads as a number: `=` compares a
  number with text by their text, which a join doesn't.
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
  `step(?h, ?x), attr(?x, ?v)`). Once something is bound, a generator sharing no variable with it
  waits while a literal that does could run (`joinedLater`, #402: Declaire's x05 ran
  `loc.home(?f, "oneauth")` once per `loc.home(?c, "goapplib")` before `calls(?c, ?f)`); with nothing
  bound, constants still pick the first. A variable only its all-free mode would take counts as unbound
  (`generatorBound`, #304: agni's `net.reaches(?n, ?rn, ?h)` with only `?rn` bound walked the board
  once per TVS pin); a constant still counts, since it binds the call once. A generator also waits in `deferComparisons`, as an expression does,
  for a relation still to run that binds a variable it reads where its `Types` say number
  (`numbersRead`, #244), so it reads the spelling they agree on; one typing nothing, as most hosts'
  do, is never held. Naive doesn't plan: it calls a generator where it is
  written, mode or not, with the unbound inputs `Bound: false`, so a generator must refuse what it
  can't run without (`str.distance` errors like a filter, #77) rather than read the zero value; a
  `both()` test of a generator-first body catches it as Naive answering differently. An engine walk
  (closure, paths) is the exception: `deferComparisons` holds it until its mode and bounds are bound
  (#407), since Naive erring on a bound written after the call failed a body the plan answered. A filter is
  refused the same way when nothing in its body binds an argument (`checkFilters`, #215, after
  `checkRules` so a redefinition reports first), and `deferComparisons` holds one until its arguments
  are bound, so Naive answers a filter written before its binder. Relations tied on bound arguments (at least one) put a derived call bound only by constants or a
  one-value equality first (`seeded`, #392: demand then walks back from the constant, where after
  `node(?t, "Func")` it was asked once per function) when the other would bind one of its variables
  and reads more than a tuple (`bindsFor`), and go by
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
