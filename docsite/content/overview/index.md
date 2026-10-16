---
title: "What jaala is"
description: "A Datalog engine you embed in a Go program, which knows nothing about your domain and cites the facts behind every answer."
---

jaala is a Datalog engine for graph-shaped data. A host program, written in Go, gives it facts (parts on nets, edges in a graph, tests and the code they cover) and asks questions as Datalog rules and a goal. jaala works out the answer and names the facts behind every row. It started as the query engine inside [agni](https://github.com/panyam/agni), an EDA tool, and we pulled it out so other graph tools could share it. [Declaire](https://github.com/panyam/declaire) is the second host.

## A query

Here's a reachability question over a directed graph, the [`graph`]({{ datasetURL "graph" }}) dataset. The two rules define `reach`, and the line after `=>` is the goal: everything `a` reaches.

{{ demo "demos/overview/reach.yaml" }}

The graph has the edges a→b, b→c and c→d, so the answer is `b`, `c` and `d`, and the row for `d` cites the three edges on the way there. The site works that answer out when it's built, by running the query on jaala, so what you see is what the engine says.

## What the language has

- Rules that define derived relations, and recursion through them to a fixpoint.
- Stratified negation, so `not covered(?n)` reads a relation that's already complete.
- Aggregation in the answer (`count`, `sum`, `min`, `max`, `list`, with `distinct` and `having`) and in a rule's head.
- `order by`, `limit` and `offset`, over an answer that's sorted the same way every time.
- Types on a relation's arguments, declared or inferred, so a constant of the wrong type is refused rather than matching nothing without an error.
- Arithmetic and units, `?p = ?v * ?i` and `?w > 3.3V`, written as functions that run in whichever direction the query needs ([arithmetic and units]({{.Site.PathPrefix}}/guide/arithmetic/)).

## Provenance

Every answer row carries the citations of the facts that produced it, so a host can point at the evidence: the net, the part, the line in the source file. Asked for witnesses, jaala also returns the tree of rules and facts that derived the row.

## Where the facts come from

A host serves its facts through a `Source`, and the simplest one holds them in memory. A source over a database or an index can answer only what a query binds, and can join, count, page or walk itself, so a question asks for what it needs rather than loading everything, and a federation spreads one question over several stores ([facts from stores]({{.Site.PathPrefix}}/guide/stores/)). A source can serve several versions of its facts, the revisions of a design or the commits of a repo, and one base answers at any of them, across all of them at once, or says what changed between two ([versions]({{.Site.PathPrefix}}/guide/versions/)). And a host's own Go can run inside a query as a predicate or a generator, for what's easier to compute than to serve ([predicates and generators]({{.Site.PathPrefix}}/guide/predicates/)).

## Three packages

jaala comes as three packages, layered one way.

- `ns` is the vocabulary: the tree of names a query can call, each with its signature. A name is a base relation whose facts a host's `Source` serves, a predicate the host computes, or a derived relation a module defines. It doesn't parse or evaluate anything, which is the engine's job.
- `stdlib` is the standard vocabulary: string tests like `str.contains` and `str.glob`, and `absent`.
- `datalog` is the engine. It parses queries, links the modules a query names, and evaluates it over a vocabulary paired with a `Source`.

All three use only the Go standard library, and they build for `GOOS=js GOARCH=wasm`, which is how the examples on this site will run in your browser.

## Two evaluators

`Naive` evaluates rules in the order they're written and repeats every rule until nothing changes. It's slow on purpose, and fairly easy to convince yourself is right, which is why every faster strategy is checked against it. `SemiNaive` is the one to use. It only revisits what changed in the last round, derives just what a query's constants ask for, and plans each rule body, so the order a rule is written in doesn't change what it costs.

To learn the language one idea at a time, start with the [tutorials]({{.Site.PathPrefix}}/tutorials/), look things up in the [guide]({{.Site.PathPrefix}}/guide/) and the [reference]({{.Site.PathPrefix}}/reference/), and read how it works in [architecture]({{.Site.PathPrefix}}/architecture/).

This site documents jaala at the commit shown in the footer, which is usually a little ahead of the latest release.
