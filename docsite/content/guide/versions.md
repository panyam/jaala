---
title: "Versions"
description: "One base over several versions of the facts: answering at a revision with At, across many at once with Across, and what changed between two with Diff."
prev: {url: "/jaala/guide/absent-values/", title: "Absent values"}
next: {url: "/jaala/guide/plain-language/", title: "Asking through a model"}
---

A host often keeps several versions of the same facts: the revisions of a design, or the commits of a repo. Most facts don't change between two of them, so serving each version as its own source repeats nearly everything. A source implementing `ns.TimedSource` serves each fact once, with the versions it holds in, and one base answers at any of them ([#179](https://github.com/panyam/jaala/issues/179)).

```go
{{ includeFileText "examples/guide/versions_test.go" }}
```

## Versions and version sets

A version is an `ns.Version`, a number the host assigns. jaala only compares and collects versions, so what they stand for is up to the host. The versions a fact holds in are an `ns.VersionSet`, and `ns` has two kinds. An `IntervalSet` holds ranges, for linear history, where a fact usually holds from one revision until another: `ns.Span(1, 4)` is revisions 1 to 3, and `ns.Since(1)` is revision 1 and every one after it. A `BitSet` holds bits, for history that branches, where a fact's commits can be anywhere: `ns.Bits(0, 5, 9)`. The two combine with each other, and an operation answers in its receiver's kind.

A fact is its values. Two versions holding the same values hold one fact, even when their citations differ, so a host whose citations change on every export, such as one embedding an export's own ids, still shares its unchanged facts. A `TimedTuple` carries its citations per version, and the engine shows the ones for the version asked about. Deciding which facts are the same across versions is the host's job: jaala never guesses that two different values name one thing.

## `At`

`datalog.At(v)` answers a query at version `v`, over exactly the facts holding there, each with `v`'s citations. It answers as a base over a source serving only those facts would, with every evaluator, option and witness. Each version's facts, indexes and derived relations are cached apart from every other's, and `Base.LimitVersions` bounds how many versions' caches a base keeps, 8 by default. The facts themselves are read from the source once for every version. A generator called under `At` is handed the version's facts as its source, and a context carrying the version (`ns.VersionOf`), so one reading versioned data of its own can read the same version.

A query without `At` reads a timed source as any other, through `Tuples`, so it sees whichever facts the source serves there, usually its latest version. `ns.TimedMemSource` serves its last version that way.

A timed source that is also `ns.Versioned` drops every version's caches when its version moves, as it would its own, so a host adding a new revision bumps the version.

## Versions from stores

A timed source read through `TimedTuples` hands over every fact of a relation, in every version, the first time a version reads it. That suits facts held in memory. For versions kept in a store, the source implements `ns.SourceAt` as well, returning version `v`'s facts as a source of its own ([#312](https://github.com/panyam/jaala/issues/312)). `At(v)` then reads that source as it reads any other, so a lookup, a pushed join, a walk, a summary or a page goes to the version's store, and `TimedTuples` isn't read at all. The engine asks `SourceAt` once per version whose caches it keeps, so a source over one database returns a small view of it (the database and the version) rather than copying anything. A version's source that is `ns.Versioned` drops that version's caches when it moves, and what a version keeps in a `DerivedStore` is filed under the version too.

Facts often come from places with different histories, such as a design's netlist, which changes every revision, and a datasheet library, which doesn't. `Federation.Timed` makes a federation a timed source: a member that serves versions holds its facts in them, any other member holds its facts in every version, and at a version the federation is one over the members at that version, with their names, `Holds` and partitioning. Its versions are the ones every timed member serves.

`Example_atFromStores`, in the example at the top of the page, asks revision 2's store for what `api` imports, and the license comes from the member that doesn't change.

`Across` asks a different question of a store: every fact matching a call, in every version at once. A timed source that can answer it implements `ns.TimedLookupSource`, whose `LookupTimed` takes the bound values as `Lookup` does and returns `TimedTuple`s, each fact once with the versions it holds in ([#317](https://github.com/panyam/jaala/issues/317)). `Across` then asks it for what a query binds rather than reading `TimedTuples` whole, and `ns.TimedLookupRelations` says which relations it looks up, when not all of them. A timed federation looks a call up in the members whose `Holds` allow it, a timed member through `LookupTimed` and any other through `Lookup`, its facts holding in every version. `Example_acrossFromStores` asks the history for what each package imports, a call per package the recursion reaches, and answers the rows reading it whole gives.

## `Across`

`datalog.Across(vs)` evaluates a query once over every version in `vs`, and each row carries the versions it holds in, in `Row.Versions` ([#180](https://github.com/panyam/jaala/issues/180)). The rows holding version `v` are exactly the rows `At(v)` answers. A join holds in the versions all its facts hold in, a tuple derived two ways holds in the versions either holds in, and a `not` holds in the versions the negated relation doesn't. So when most facts hold in most versions, a recursive relation is worked out about once rather than once per version. On a points-to analysis over 50 revisions with 4% of its facts changing somewhere, `Across` does a ninth of the work of 50 `At`s and takes about a fifth of the time (`BenchmarkVersions`).

A row that holds in some versions and not others says when it started or stopped holding, like `log` above, which `app` depends on from revision 1. Where a row is derived differently in two versions, it's answered once per derivation, each copy with the versions it holds that way in, so each version's copy is the row `At` gives it, witness included. A `limit` and `offset` page each version's rows separately, as `At` would.

Citations differ per version, so a row's `Cites` are its facts' citations in the latest version it holds in, and `Row.CitesAt(v)` gives another version's. Likewise `Witness.At(v)` turns a row's witness into the one `At(v)` gives. A generator, filter or comparison holds in every version, since jaala can't know what a host's Go reads, so a generator reading versioned data of its own belongs under `At`. `Across` reads the source's facts through `TimedTuples`, or `LookupTimed` for a bound call, without pushdown, walks or streaming, and keeps nothing for a later query, neither the facts it looked up nor a derived relation.

An aggregate across versions is cut at the versions where its inputs change ([#181](https://github.com/panyam/jaala/issues/181)). Each group is reduced per stretch of versions over the bindings holding in all of them, and stretches that reduce to the same row from the same facts share one. `app` depends on two packages in every revision above, but on `util` in revision 0 and `log` after, so its count is two rows, each citing its own facts, and a count over facts that don't change between revisions is one. A group with no rows in some versions has no row there, and a goal with no group column answers its row over nothing (a count of 0) in the versions where nothing matched, as `At` does. That covers aggregates in the goal, in a rule head and in a body, `having` included.

## `Diff`

`Base.Diff(ctx, rel, from, to)` lists the facts of a base relation that hold in `to` and not `from`, with `to`'s citations, and those that held in `from` and not `to`, with `from`'s. Facts are compared as the engine reads them, as the relation's declared types with units folded, so `"1.0"` served in one version and `1` in the next is one unchanged number. A fact whose citations changed and nothing else isn't in either list.

## Sessions

A session's edits are checked as if they were applied before anything is saved, and then kept as a new version or thrown away ([#288](https://github.com/panyam/jaala/issues/288)). The engine never writes to a source, so an edit is an `ns.Changes`, the facts to add to and remove from each base relation ([#287](https://github.com/panyam/jaala/issues/287)), and `ns.Overlay(src, changes)` is a source serving `src`'s facts minus the ones removed, plus the ones added. A base over it answers with the session's own writes, as `Example_session` shows, and the base over `src` never sees them. Throwing the session away means dropping the overlay; an undo is the change sets before the last one, composed with `Changes.Then`.

A fact is named by its values, read as its argument's declared type as a lookup reads it, so a removal names a fact as the store serves it, units and all. The last operation on a fact wins, and a fact the change set names is replaced wherever the store holds it, every copy, so adding a fact the store holds already holds it once, with the session's citations.

An overlay passes on every store interface its source implements. A lookup or a batch goes to the store, and the facts the session adds join every answer for that relation. A pushed conjunction, a walk or a summary goes to the store only when it reads no relation the session touched; otherwise the planner solves it literal by literal through lookups, since the store's own join can't see the session. Each overlay's `Version` is the source's joined with a fingerprint of its change set, so a base kept over a session drops what it held when an edit makes a new overlay.

`Effective` is what a session changes: the removals of facts the store holds and the additions of facts it doesn't, which is what a host rechecks. A commit keeps them as a new version. `TimedMemSource.Commit(changes, from, to)` adds version `to` holding `from`'s facts with the changes applied, so `Diff` from `from` to `to` lists the session's effective changes, and a host's own timed store commits the same way. To edit an older version, overlay that version's source (`SourceAt`) rather than the timed source, whose plain reads serve its latest.
