---
title: "Evaluators"
description: "Naive and SemiNaive: what each evaluator does, and what it guarantees about its answers."
---

An evaluator answers a query over a `Base`. The [architecture]({{.Site.PathPrefix}}/architecture/evaluation/) section explains how. jaala has two, and three ways to run them, which all give the same answer and differ in how much work they do getting there.

- **`SemiNaive{}`** is the one you'll want. In each round of a recursive fixpoint it only revisits what the last round added. It derives only what a query's constants ask for (magic sets), and it plans each rule body so the order a rule is written in doesn't change what it costs.
- **`SemiNaive{WrittenOrder: true}`** keeps the semi-naive fixpoint and turns the rewrites off, running each body in the order it's written.
- **`Naive{}`** repeats every rule until a round adds nothing, in written order. It's slow on purpose and fairly simple to trust, so it's the reference every faster strategy is checked against.

```go
{{ includeFileText "examples/reference/evaluators_test.go" }}
```

## What's guaranteed

- **The same rows, in the same order.** All three answer every query with the same rows, sorted the same way, which jaala's own tests check on every change, against its hand-written programs and a few hundred generated ones, and `selfcheck.sh` against thousands more.
- **The same errors from the written order.** `SemiNaive{WrittenOrder: true}` fails exactly when `Naive` does, with the same message. The planned `SemiNaive{}` gives `Naive`'s error whenever it fails, and can succeed where written order stops (a body written in an order that can't run as written, which planning fixes).
- **Citations from one derivation.** A row cites the facts of the derivation that found it first. When a fact is reachable two ways, the evaluators can find different ones first, so two evaluators can cite different, equally valid evidence for the same row. `CanonicalCites()` makes all three cite the same one, each row's shortest derivation ([#22](https://github.com/panyam/jaala/issues/22), see [provenance]({{.Site.PathPrefix}}/architecture/provenance/)).
- **Bounded by `Budget`.** Each counts its work the same way on every run, so a budget that passes once passes every time, for that evaluator.
