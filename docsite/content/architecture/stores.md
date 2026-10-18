---
title: "Stores"
description: "How the planned evaluator decides what to ask a host's store, where what comes back is kept, and why a store's precision can cost calls but never answers."
prev: {url: "/jaala/architecture/rewrites/", title: "Rewrites"}
next: {url: "/jaala/architecture/provenance/", title: "Provenance"}
---

The [facts from stores]({{.Site.PathPrefix}}/guide/stores/) guide page says what each store interface asks of a host. This page is about the other side: when the engine asks, what it does with the answer, and how jaala checks that a store can only change what a query costs.

```go
{{ includeFileText "examples/architecture/stores_test.go" }}
```

## One promise

Every store interface has the same contract as `Lookup`. A store may return more than was asked, never less, and jaala reads each tuple as its argument's declared type and checks it against what was asked: the literal, the comparisons, the filters, the walk's constants. So a store that ignores a comparison, a lookup returning a whole relation, or a federation member whose `Holds` answers true when it isn't sure costs calls and shipping, and the answer, its citations and its witnesses come out the same. `Example_aSloppyStoreCostsShippingNotAnswers` above ships two hundred times the facts and answers the same nine rows.

What jaala can't check is a store leaving something out, so each interface is opt-in, and a store says what it takes before it's sent it. `Pushes`, `Walks` and `Summarizes` are asked while planning and must be cheap. A field added to a struct a host already implements is ignored by every host built before it, so behaviour that depends on the host acting on a new field gets a new interface instead. That's why a pushed join carries filters only to a store naming them in `ns.FilterSource` ([#297](https://github.com/panyam/jaala/issues/297)): a store from before it accepted joins and ignored the field, and a group grown past its filters joined every row they'd have dropped.

## What goes to a store, and when

Only the planned `SemiNaive` hands work to a store beyond lookups. `Naive` and the written-order `SemiNaive` look up bound calls and read the rest whole, which keeps them the reference the corpus compares against. The planned evaluator decides in three places.

While planning, after the [rewrites]({{.Site.PathPrefix}}/architecture/rewrites/), it groups a body's base literals into conjunctions a `PushSource` accepts (`pushConjunctions`). A group starts at a pushable literal and takes each later one sharing a variable with it, and stops at a literal outside it that only filters what it bound, so the store isn't asked to join rows that filter would drop ([#291](https://github.com/panyam/jaala/issues/291)). Variables bound before the group become its `Inputs`. Then it records each recursive rule whose step a `WalkSource` could follow (`findWalks`), and marks a goal whose whole body a store could evaluate, for a summary or a page (`goalGroup`).

Before solving a body, it collects what the body will ask (`prefetch`). It runs the body up to each looked-up literal and asks for the values no cache holds in batches of up to 1024 (`LookupBatch`), asks a pushed group's store for a batch of its inputs' bindings, and turns a probe a walk covers into one `Walk` from the values not yet reached. `solve` then finds everything in the caches, so it stays depth-first and the goal keeps streaming into its answer.

A call that names its fact, `edge(?a, ?b) as ?r` ([#279](https://github.com/panyam/jaala/issues/279)), is rewritten before anything else runs into a call to a relation the store never sees, `edge`'s fact relation, whose last argument is the id. The base reads it as `edge` with each tuple's id after its values: whole, when it reads `edge` whole, and as `edge`'s own lookups and batches, kept under `edge`'s keys, otherwise. A store is never asked to push, walk, summarize or stream one, so a fact literal is solved by the engine while the literals around it still go to the store.

While solving, a goal's first literal can read a streamed relation as it arrives, keeping none of it (`StreamSource`, [#264](https://github.com/panyam/jaala/issues/264)), and anything a prefetch missed is looked up as before. So batching, pushing and walking each only ever replace calls that would otherwise have been made one at a time. When one can't apply, the query falls back to those calls, never to a different answer.

## Where answers are kept

What a store returns lives in two tiers. The `Eval` keeps everything it asked for its whole run, and the `Base` keeps lookups, pushes and walks for later queries, up to a million tuples by default, least recently used first (`LimitLookupCache`, [#212](https://github.com/panyam/jaala/issues/212)). A pushed group's rows are filed under its conjunction with a binding's values, and a walk files each value it reached as that value's own complete lookup, which is how the fixpoint finds them without knowing they came from one call. A read that failed or was stopped by the `Budget` isn't kept, and a `Versioned` source moving drops all of it.

A lookup counts one unit of work against the `Budget`. A tuple a store streams or pushes is charged to the `Budget` as it arrives too, as a read beside the query's work, so a budgeted query stops having held at most that many tuples ([#233](https://github.com/panyam/jaala/issues/233)). `Explain` reports the calls (`Lookups`, `Batches`, `Pushes`, `Walks`) and what they fetched, and leaves the reads out of `Work`, which the work baselines compare.

## Federations

A `Federation` is itself a source over several, so the engine sees one store. It routes each call to the members whose `Holds` allows the bound values, batches per member, and walks across members, each walking a value once. A join across members is pushed only by a partitioned federation: each member answers the matches it holds whole plus where each one leaves it (`Conjunction.Partial`), and the federation continues those prefixes in the members holding the next fact. Routing leans on `Holds` never answering false for fewer bound values where more would answer true ([#307](https://github.com/panyam/jaala/issues/307)). A timed federation serves each version as a federation of its members at that version ([#312](https://github.com/panyam/jaala/issues/312)).

## Across versions

`At(v)` reads a version through the version's own source, so everything above applies there. `Across` evaluates once over facts annotated with the versions they hold in, and each tuple's versions travel with it: a relation read whole keeps them by the tuple's place in the read, and a lookup through `ns.TimedLookupSource` keeps them under its lookup key, beside the tuples ([#317](https://github.com/panyam/jaala/issues/317)). A store is asked nothing else under `Across`: no batch, push, walk or stream, since each would need a twin returning versions too. An `Across` `Eval` builds its own base, so what it looked up is kept for its run and dropped after.

## How it's checked

The generated corpus ([testing]({{.Site.PathPrefix}}/architecture/testing/)) runs every program over each kind of store and requires the answer Naive gives reading the facts whole: a looking-up and batching store, a three-member federation indexing only first arguments, a pushing store (half of them refusing comparisons), a partitioned federation, a streaming source, and each version served as a store. Goals a store could summarize or page, and walks, are rare in generated programs, so the corpus also runs fixed questions of those shapes per graph. Each step has a control counting how often the corpus really reached it, so a store path that quietly stopped being taken fails rather than passing. `BenchmarkStores` measures what the corpus can't: calls, unindexed probes and fetched tuples as the number of stores grows.
