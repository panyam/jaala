---
title: "Host options"
description: "Bind, Budget, Explain and the context: how a Go host gives a query its values, caps its work, sees where the work went, and stops it. Then pages and tables, sharing a base, and where a parse error is."
prev: {url: "/jaala/guide/ordering/", title: "Ordering"}
next: {url: "/jaala/guide/predicates/", title: "Predicates and generators"}
---

A host runs a query with `Eval`, and passes options to it. This page covers the ones a host uses most. The [Go tutorial]({{.Site.PathPrefix}}/tutorials/05-go-host/) builds the vocabulary and base they run against, and shows the fourth, `Witnesses`. Every code block here is a Go `Example` from `docsite/examples/guide/`, which `go test` runs on every build of this site and checks against its `// Output:` comment.

The examples share a source and a vocabulary, built the same way as in the tutorial, with a `deps` module of rules:

```go
{{ includeFileText "examples/guide/vocabulary_test.go" }}
```

## `Bind`, `Budget` and the context

```go
{{ includeFileText "examples/guide/options_test.go" }}
```

`Bind` gives a goal variable its value from Go, so the query text stays fixed and nobody builds queries out of strings. A bound variable behaves as if its value were written into the goal, and that includes counting as the anchor of a `not` (see [negation]({{.Site.PathPrefix}}/guide/negation/#a-not-has-to-be-anchored)). The answer still has the bound column, filled with the value. Binding a variable the goal doesn't use is an error, since it's pretty much always a typo, and a typo there would otherwise answer the unbound question without a word.

A variable can be bound to several values too, which is how a host asks one question about a whole selection, such as every package a user ticked. The variable then ranges over the values, as if the goal were joined with a relation holding exactly them, so the answer is the union, and an aggregate like `count(distinct ?d)` counts across the whole set, which asking once per value can't do. Demand starts from every value, so a recursive relation is still worked out only for the values asked, and one query costs about what the separate ones would. Each value is checked as a single one is, and an error names the one refused. A variable bound to no values matches nothing: the answer is empty, or the one row an aggregate gives over nothing (a count of 0). A set of one value is the same as binding that value.

`Budget` caps the work one `Eval` does, counted the same way on every run, so a budget that passes once passes every time. `Base.Work` reports what a query used, which is how to size a budget from your real queries. It bounds work, not memory. What a query holds grows with its answer rather than its work: a goal keeps one row per distinct answer, or one running total per group for an aggregate, not every combination it found on the way ([#120](https://github.com/panyam/jaala/issues/120)), so a count over a million bindings holds one group. The derived relations a query's rules produce are held in full, though, and those do grow with the work. So is a relation the source hands over as one slice: it's all in memory before the budget sees any of it. A source that implements `ns.StreamSource` hands a relation over one fact at a time instead, and each fact counts against the `Budget` as it arrives, so a query over a relation bigger than its budget stops having held at most that many facts ([#233](https://github.com/panyam/jaala/issues/233)). Those facts aren't work: `Base.Work` and `Explain` count what the query did with them, and only the budget, and the `BudgetExceeded` it stops with, adds the read. A read the budget stopped isn't kept, so the next query reads the relation again. A `Federation` streams its members in turn, so it stops before the next member's read even when its members return slices. When the goal is the only reader of a streamed relation (no rule reads it, and the goal reads it once, first), its facts go straight into the answer as they arrive and aren't kept ([#264](https://github.com/panyam/jaala/issues/264)): a `limit` without an `order by` stops the read at its rows, and a count holds its one group rather than the relation. The next query that needs the relation reads it again, unless something else already kept it. A relation the source can look up streams this way only under such a `limit`, so a repeated question over it still asks the source nothing.

The context reaches every `Eval`. When it's cancelled or its deadline passes, the `Eval` stops at its next check, which comes every 1024 units of work, and returns an error that wraps the context's own, so `errors.Is(err, context.DeadlineExceeded)` works. It also reaches a host's own Go code running inside a query, its generators and sources, so those can stop too.

## Pages, totals and tables

`After`, `Total` and `EvalTable` are for a host showing a big answer a page at a time, such as a viewer listing everything on a net ([#194](https://github.com/panyam/jaala/issues/194), [#195](https://github.com/panyam/jaala/issues/195)). `Example_page` and `Example_table` in the code block above show them.

`After` takes the last row of the page before, as the host got it, and answers only what comes after it in the query's `order by`, as the query's own `after` clause would ([ordering]({{.Site.PathPrefix}}/guide/ordering/#paging-with-after)). With a `limit`, each page holds only its own rows while it runs, and a page stays put when the facts change between requests. `Total` fills an `int` with the number of rows the whole answer has before `after`, `offset` and `limit`, so the page can say how many it left out. Counting needs the whole answer, so a `limit` without an `order by` doesn't stop early when `Total` is asked for.

`EvalTable` returns the same answer as `Eval`, as a `Table` of columns and rows of values, with each row's citations, witness and reasons beside it. `Eval` gives every row a map of its own, which for a small row costs several times its values, so a host serializing a large answer is better off with the table. `Table.Row(i)` is row `i` as `Eval` returns it. `Explain`'s `Report.Held` says how many rows (or groups) the goal held at its peak.

## Explaining a slow query

`Explain` fills a `Report` with what an `Eval` did, so a slow query shows where its work went rather than one total to bisect by hand ([#147](https://github.com/panyam/jaala/issues/147)). `Example_explain` in the code block above prints part of it. `Report.String` prints all of it, and the struct has JSON tags, so a host can log it beside a slow query or return it with an answer.

The report lists each derived relation the query reached, and whether it was evaluated in full, under demand (with how many values were demanded), as a factored walk, inlined into its callers, or reused from what the `Base` kept from an earlier query. Each comes with its tuples, rounds and work.

It then shows the goal and each rule body in the order it actually ran, after jaala's rewrites, labelled with the rule as you wrote it. Every literal says how it was read (an index on which arguments, a scan, a generator or a filter), how often solving reached it, how many bindings it passed on, and how many candidates it examined. A literal reached thousands of times is usually the place to look.

Last come the base relations it read, whether the `Base` already held them, and the indexes it probed, with whether this `Eval` built them. For a source that looks facts up ([facts from stores]({{.Site.PathPrefix}}/guide/stores/)), each relation also says how many lookups it took, the tuples they fetched, and how many calls were answered again from what an earlier lookup in the same `Eval` returned. `Report.Fetched` totals the tuples the source handed this `Eval`, and `Report.Lookups` the calls.

`Work` is what the `Eval` spent, which is what a `Budget` caps, so it depends on what ran on the `Base` before: a query reusing a relation an earlier one kept costs less. `Report.Cold` is the same query's cost on a base that kept nothing, which doesn't move from run to run, so it's the number to compare two queries or plans by, or to suggest a budget from. When the query reused something, `Explain` finds it by running the query again on a copy of the base that keeps nothing. That run doesn't count toward `Base.Work`, but it does read the source again.

The report is filled even when the `Eval` fails, so a query its `Budget` stopped still shows what spent the budget. Asking for it costs time in proportion to the rules and literals a query runs. Not asking costs nothing measurable, which is why it's off by default.

## Sharing a base

A `Base` pairs a vocabulary with one source's facts, and caches and indexes what it reads. It's safe to share between `Eval`s running at the same time, since each `Eval` keeps its own state. Build one per dataset, and reuse it for every query over that dataset, which mostly comes free once the vocabulary is built at startup.

It also keeps the derived relations a query evaluated in full, so the next query calling the same relation reads it instead of deriving it again ([#140](https://github.com/panyam/jaala/issues/140)). A library member that three workbook tabs all call is derived once. The key is the relation's rules and everything they read, so a query that sends its own definition of a relation derives its own. A relation derived only for the values one query asked about isn't kept, since it's partial. Only `SemiNaive` uses the cache, and not under `Witnesses`. `Base.LimitDerivedCache` sets how many derived tuples the base keeps (about a million by default, and 0 turns it off), dropping the relations used least recently first.

A base's memory lasts as long as the base. A host that builds a base per request, runs several processes, or wants its own tools to read what a rule derives can also give the base a store with `Base.KeepDerivedIn` ([#220](https://github.com/panyam/jaala/issues/220)). The store implements `ns.DerivedStore`, two calls: `Derived` hands back a relation it holds, and `KeepDerived` takes one a query evaluated in full. Each relation is filed under an `ns.DerivedKey`: its name, the text of its rules and everything they read (the same key the base's memory uses), and the source's `Version`. A second base over the same version then reads the relation from the store instead of deriving it, which is what a federation of stores wants when the same rules run against the same members again. The store is read when the base's memory misses, so `LimitDerivedCache(0)` leaves it as the only tier. Only a `Versioned` source's relations go there, since an unversioned source's facts have nothing to file them under, and the same evaluators and options use it as use the memory. A store that fails, or hands back tuples of the wrong arity, costs the work of deriving and never changes an answer. `SemiNaive.Materialize` derives the relations you name in full and keeps them, with everything they read, ahead of any query, and returns the store's error if it refused one. It also keeps a relation with one rule, which a query would have folded into its caller rather than derived. `ns.MemDerivedStore` is one held in memory, and its `Relation(rel, version)` reads a kept relation back as facts.

All of this assumes the source's facts don't change while the base is kept. If yours can, have the source implement `ns.Versioned`, whose `Version` string changes whenever the facts do. Each `Eval` reads it first, and when it has moved, the base drops everything it cached and reads the source again. A host that changes facts in place without a version calls `Base.Forget` instead. A generator whose answers can change while the facts don't (one reading the clock, say) sets `Volatile` on its `ns.Builtin`, and no relation reading it is kept.

## Where a parse error is

A query that doesn't parse returns a `*datalog.ParseError`, which carries where the piece of text it's about starts: `Line` and `Column`, counted from 1 in characters rather than bytes, and `Offset`, in bytes from the start ([#167](https://github.com/panyam/jaala/issues/167)). `Example_parseError` above finds it with `errors.As`. `Error()` is the message alone, so a host matching on a message's words sees the same text it always did. Errors from checking a parsed query (`Validate`, an unknown relation, a wrong arity) don't carry a position yet.
