---
title: "Provenance"
description: "Where an answer's citations and witnesses come from, what they're guaranteed to be, and where two evaluators can differ."
prev: {url: "/jaala/architecture/rewrites/", title: "Rewrites"}
next: {url: "/jaala/architecture/testing/", title: "Testing"}
---

Every answer row carries evidence: the citations of the facts that produced it, and on request a witness tree. That evidence is why jaala's hosts can point at a net or a line of a file and say "because of this", and it's mostly what makes an answer worth acting on.

## Citations

A source gives each tuple its citations, opaque strings the host chooses (a file and line, a part reference). A derived tuple carries the citations of the facts it was derived from, and an answer row the union of its literals', as a sorted set (`Row.Cites`). The overview's [first example]({{.Site.PathPrefix}}/overview/#a-query) shows a row citing the three edges on its path.

A tuple keeps the citations of the derivation that found it first. When it can be derived two ways, which one comes first depends on the order of evaluation, so two evaluators can cite different evidence for the same row, both valid:

```go
{{ includeFileText "examples/architecture/provenance_test.go" }}
```

## Canonical citations

`CanonicalCites()` makes the evidence the same whichever evaluator answers ([#22](https://github.com/panyam/jaala/issues/22)). Each tuple keeps its shortest derivation, the one with the fewest rule steps above the facts, which is also the easiest to check by hand. When two are equally short, it keeps the one whose rule comes first by its written text, and then the one whose body facts come first by value, in the order they're written. The shortest derivation comes out the same however it's found, and the tie-breaks only read what was derived, so every evaluator lands on the same one:

```go
{{ includeFileText "examples/architecture/canonical_test.go" }}
```

It costs a bit. A tuple derived again is compared with the one kept rather than dropped, and when a later round finds a shorter derivation, whatever was derived from the tuple is derived again, so that shortens too. It also keeps the planned evaluator from inlining, factoring and storing body prefixes, which would change the steps it counts or merge derivations it has to tell apart. On jaala's baselines that's about the same work for a closure, and half as much again for a points-to analysis, with up to four times the memory, since it records each tuple's derivation to compare them. A host that needs stable evidence, for goldens or for a review someone signs off, turns it on, and one that only needs answers can leave it off. With `Witnesses()` as well, the witness is the canonical derivation too. jaala's tests run every query under it and require the same rows, citations and witnesses from all three evaluators.

## What the rewrites do to citations

The demand rewrite adds relations recording what a query asked for. Those tuples carry no citations, since otherwise an answer would cite the facts that worked out someone else's demand. Two of the relations it adds do keep theirs:

- **a factored walk** keeps citations, so an answer cites one whole path;
- **a supplementary relation**, a stored body prefix, keeps the citations of the literals it stands for, and under `Witnesses()` their witnesses too, which jaala splices back into place.

## Witnesses

`Witnesses()` records, for each row, a tree of the rules that fired and the facts at their leaves, as the [Go tutorial]({{.Site.PathPrefix}}/tutorials/05-go-host/#witnesses) shows. The tree follows the program as written: a rewrite that rebuilds a literal or a rule keeps the literal's written position and the rule's written text, so the tree shows rules the author would recognise, in the order they wrote them. Inlining and factoring are off for a witnessed evaluation, so every relation keeps its own node. Demand isn't: a rule that would have been inlined into the goal passes what its body binds on to what it calls, as the inlined goal would have.
