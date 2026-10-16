---
title: "Versions"
description: "One base over several versions of the facts: answering at a revision with At, and what changed between two with Diff."
prev: {url: "/jaala/guide/absent-values/", title: "Absent values"}
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

## `Diff`

`Base.Diff(ctx, rel, from, to)` lists the facts of a base relation that hold in `to` and not `from`, with `to`'s citations, and those that held in `from` and not `to`, with `from`'s. Facts are compared as the engine reads them, as the relation's declared types with units folded, so `"1.0"` served in one version and `1` in the next is one unchanged number. A fact whose citations changed and nothing else isn't in either list.
