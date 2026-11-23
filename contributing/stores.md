# Stores

How the engine asks a Source: lookups, streams, batches, overlays, federations, versions, indexes, pushdown, summaries and walks.

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
- **A session is an `ns.Overlay` over a Source, with its edits an `ns.Changes`** (`ns/overlay.go`,
  `ns/changes.go`, #287, #288). A fact is its values, keyed by `joinKey` under the argument's
  declared type (`typedKey`), the last operation on one wins, and a named fact replaces every copy.
  The overlay implements every store interface: lookups and batches go to the base and get every
  added fact of the relation (allowed by Lookup's contract, and nothing in `ns` folds units), while
  a push, walk or summary goes to the base only when the session touched none of its relations.
  `Version` is the base's plus an FNV fingerprint of the change set, so a session's Base drops its
  caches per edit. `TimedMemSource.Commit` is the reference commit and is `Versioned` for it; a fact
  added with an open range has the new version taken out when the commit drops it. The corpus runs
  every program over a session undoing edits to its facts (`agreeOverlaid`, over a pushing and a
  batching store, with a control per path). A change set's JSON (`ns/wire.go`, #417) replays to an
  identical set, overlay `Version` included; its values are a superset of a list's JSON (#283) with
  an object form for a unit, a non-canonical spelling and NaN, and `ns.Session` carries the host's
  base version. Compiled statements (#404) travel as change sets, never as text.
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
  `walkedPrograms` is its control; it walks a bounded closure and paths call too
  (`walkedClosurePrograms`), and `fetchedWalkPrograms` and `federatedWalkPrograms` count the corpus's
  own closures batched ahead (#396).
