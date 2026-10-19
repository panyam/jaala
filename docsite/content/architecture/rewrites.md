---
title: "Rewrites"
description: "What the planned SemiNaive does to a query before running it (inlining, demand, factoring, planning), and when each one stands aside."
prev: {url: "/jaala/architecture/evaluation/", title: "Evaluation"}
next: {url: "/jaala/architecture/stores/", title: "Stores"}
---

The planned `SemiNaive{}` rewrites a query before running it, in three steps: inlining, then demand, then planning. Each produces ordinary rules, so the fixpoint, strata and checks apply unchanged, which keeps the rewrites fairly easy to reason about. Every step has cases where it stands aside, and we've come to think of those as part of the design, since a rewrite that makes one query faster can make another slower or wrong. Most of them were found the hard way, by a test. `SemiNaive{WrittenOrder: true}` turns all three off.

```go
{{ includeFileText "examples/architecture/rewrites_test.go" }}
```

## Before rewriting

jaala drops the rules the goal can't reach, since they can't change the answer and a rewrite renaming what they read would leave them reading nothing ([#90](https://github.com/panyam/jaala/issues/90)). They've already been checked by then, so a mistake in one is still reported. The second example above shows the check that comes first: the error names `depends_on`, the relation as written, though the rewrite would have renamed it.

Before that, a goal's equality between a variable and text binds the variable first ([#370](https://github.com/panyam/jaala/issues/370)). Agents tend to write the constant last, `covers(?t, ?h), ?h = "X"`, and planned as written that evaluates `covers` for every `?h` and keeps one. The rewrite adds a literal reading a relation of that one value, as a host's set-bound variable gets ([#132](https://github.com/panyam/jaala/issues/132)), and the planner keeps it first, so demand starts from `"X"` and a recursive call is factored as `covers(?t, "X")` would be. The equality stays and holds of every row, so the answer, its citations and its witnesses don't change. A number is left alone, since a query's number only filters and mustn't lend an answer its spelling or unit ([#227](https://github.com/panyam/jaala/issues/227)).

## Inlining

A [`graph.closure`]({{.Site.PathPrefix}}/guide/syntax/#walking-a-relation) or [`graph.paths`]({{.Site.PathPrefix}}/guide/syntax/#every-path) over a relation whose rules each read one base relation, as the Cypher compiler's `cypher_step1(?x, ?y) :- edge(?x, "CALLS", ?y)` does, walks those atoms instead of deriving the relation, so it reads only the facts its walk reaches ([#281](https://github.com/panyam/jaala/issues/281)). Witnessed, it walks the relation, whose node a witness shows.

A call to a relation with one rule, not recursive, is replaced by that rule's body, so a constant at the call reaches the literals inside, where the planner can use it. It stands aside:

- for a relation with several rules, which a single body can't hold;
- under `not`, which negates the whole body rather than each literal;
- inside an aggregate that counts bindings (`count`, `sum` or `list` without `distinct`), since a derived relation is a set and its inlined body isn't ([#4](https://github.com/panyam/jaala/issues/4));
- in the rules of a recursive relation, which the fixpoint runs every round, so an inlined join would be redone each time ([#97](https://github.com/panyam/jaala/issues/97));
- under `Witnesses()`, so every relation keeps its own node in the witness tree.

## Demand (magic sets)

A goal like `depends_on("p190", ?d)` only needs what `p190` reaches. The demand rewrite gives each called relation an adornment, a pattern of which arguments arrive bound (`bf` is bound, free), and derives the relation only for the values demanded. In the first example above, that gives the same nine rows for under 1% of the work. A rule's body before a call is stored once in a supplementary relation, so it isn't joined twice ([#54](https://github.com/panyam/jaala/issues/54)). A right-linear recursion called from a constant is factored further, reduced to the set of nodes the walk visits, so a long chain costs linear work rather than quadratic.

A rule evaluated in full, because its caller binds nothing, still demands from its constants ([#57](https://github.com/panyam/jaala/issues/57)), but not from what its earlier literals bind, since it binds them for every row. The exception is a rule that inlining would have put into the goal but didn't, because the query asked for witnesses: its body passes what it binds on to what it calls, as the goal would have ([#270](https://github.com/panyam/jaala/issues/270)).

Demand stands aside:

- at a relation whose rule aggregates, which is read in full, since a caller's binding names no value of the body;
- when demand pushed into a `not` makes the program unstratifiable, by reading negated relations in full instead ([#34](https://github.com/panyam/jaala/issues/34)), and when that still doesn't stratify, by not rewriting at all ([#93](https://github.com/panyam/jaala/issues/93));
- at a relation it would derive under two adornments, whose copies can cover the whole relation twice ([#96](https://github.com/panyam/jaala/issues/96)), which is read in full while the rest keeps its demand ([#290](https://github.com/panyam/jaala/issues/290));
- at a relation whose rules are only a host's generators, each runnable from the rule's own constants, such as `sends(?c) :- graph.reach(?c, "net/http.Get", "calls")`: read in full it calls each generator once, and demanded, once per value its callers bind ([#290](https://github.com/panyam/jaala/issues/290));
- from factoring, for a relation with no base rule to start the walk from ([#91](https://github.com/panyam/jaala/issues/91)).

## Planning

Each rule body is reordered so that what's cheap and selective runs first, given what's bound. A comparison, a test like `str.contains`, or a relation whose arguments are all bound runs as soon as it can, and a host's generator runs as soon as one of its modes is satisfied with an argument bound, so a host never has to write a body in a particular order ([#36](https://github.com/panyam/jaala/issues/36)). A generator called with nothing bound lists every value, so it runs after a relation the engine derives or holds, which costs no more read whole, and a call it would have bound for every value binds nothing and isn't demanded per value ([#291](https://github.com/panyam/jaala/issues/291), [#293](https://github.com/panyam/jaala/issues/293)). It still runs ahead of a relation the Source looks up, which read with nothing bound is a whole read from the store. A variable bound where only a generator's all-free mode would take it doesn't count as bound, since each call is then a full walk filtered on it, once per row of whatever bound it: such a call waits for a literal that binds a real input, or runs last ([#304](https://github.com/panyam/jaala/issues/304)). Once something is bound, a generator sharing no variable with it waits while a literal that does could run, since running it then is a cross product the joining literal filters afterwards: `home(?c, "a"), home(?f, "b"), calls(?c, ?f)` runs `calls` second and asks `home` for `?f`'s one home, where it listed every home of `b` once per home of `a` ([#402](https://github.com/panyam/jaala/issues/402)). With nothing bound yet, constants still pick the first generator. A generator that reads a number waits, as arithmetic does, until every relation binding that number has run, so it reads the spelling they agreed on, `1 V` rather than a bare `1` ([#244](https://github.com/panyam/jaala/issues/244)). A body the demand rewrite guarded keeps its guard first. Among the relations left, the one with the most arguments bound goes first, since it fans out least. Two with as many bound go by how many tuples a call reads from the base relation's index ([#139](https://github.com/panyam/jaala/issues/139)), so `part(?r, "capacitor")` doesn't scan every capacitor once per net when `pin(?r, ?n)` could read one net's parts. A derived call bound only by constants, or by a variable an equality fixes to one value, goes ahead of a base relation it ties with when that relation would bind one of the call's variables and its call reads more than one tuple, since demand then starts the call from those constants: after `node(?t, "Func")`, `reach(?t, "m1")` was demanded once per function and walked forward from each, where first it walks back from `m1` ([#392](https://github.com/panyam/jaala/issues/392)). A relation the Source looks up instead ([#126](https://github.com/panyam/jaala/issues/126)) isn't read to size it. If the Source says it has no index for a call's bound arguments, the call ranks as if nothing were bound, since the store scans either way, and an indexed call runs first ([#154](https://github.com/panyam/jaala/issues/154)).

## Holding the rewrites to account

Every rewrite has to answer exactly as `Naive` does. jaala's tests run every query three ways, and a few hundred generated programs through all of them on every change, thousands more in `selfcheck.sh`. The cost side is held by work baselines that fail a change making a workload more expensive. The [testing]({{.Site.PathPrefix}}/architecture/testing/) page has the detail. Most of the stand-aside rules above were found by those checks.
