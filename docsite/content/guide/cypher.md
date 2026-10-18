---
title: "Asking in Cypher"
description: "Compiling a Cypher query into a jaala program: the graph view a host writes so its relations read as nodes, relationships and properties, the part of Cypher that compiles, where the answers differ from a Cypher engine's, and what is refused and why."
prev: {url: "/jaala/guide/plain-language/", title: "Asking through a model"}
---

Agents and models have seen a lot more Cypher than jaala, so a host can let them ask in it. Package `cypher` (in its own module, `github.com/panyam/jaala/frontends/cypher`) reads a Cypher query and writes the jaala program that answers it ([#352](https://github.com/panyam/jaala/issues/352)). The program is ordinary jaala text, so `Validate`, `Eval`, `Explain` and the errors on [Asking through a model]({{.Site.PathPrefix}}/guide/plain-language/) all read it the way they read a program written by hand.

The examples use the same packages as [Host options]({{.Site.PathPrefix}}/guide/host-options/): `imports(from, to)`, `pkg(name, license)` and the `deps` module.

## The graph view

Nothing in a relation's schema says which argument is a node and which an endpoint, so the host says it, once, as a `cypher.Graph`. A `Node` names the relation holding a label's nodes, the argument holding a node's key, and the arguments holding its properties. An `Edge` names a relationship type's relation and its `From` and `To` arguments. A node is its key: `(p:Package)` binds `p` to a package's name, and `RETURN p` answers it.

A vocabulary that keeps every node in one relation, as a code graph's `node(id, kind)`, `edge(from, kind, to)` and `attr(id, key, value)` do, says so with `Fixed` (the constant that tells one label or type apart) and `Props` (the key-value relation a property is read from).

```go
{{ includeFileText "examples/guide/cypher_test.go" }}
```

The first example's `WITH` became a rule of its own, `cypher_with1`, and the `*` a recursive one. `cypher.Compile` writes a helper like that for a variable-length relationship (`cypher_reach`), a stage (`cypher_with`), the alternatives of an `OR` (`cypher_match`), a pattern or `EXISTS { }` in `WHERE` (`cypher_exists`) and a `UNION` (`cypher_union`). A `{name: 'app'}` the query doesn't read again is written into the atom, `pkg("app", _)`, so the engine plans from the constant.

## What compiles

- `MATCH` over node and relationship patterns: labels, properties written inline, either direction, several types (`[:A|B]`), and variable length (`*`, `*2..`, and a bounded `*1..3` up to 8 hops).
- `WHERE` with comparisons, `AND`, `OR`, `XOR`, `NOT`, `IS NULL`, `STARTS WITH`, `ENDS WITH`, `CONTAINS`, `=~`, `IN` over a written list, a label test (`n:Package`), a pattern, `EXISTS { }` and `COUNT { }`.
- `WITH`, its aggregates and its `WHERE`; `UNION` and `UNION ALL`; and `CALL proc(args) YIELD ...` over one of the vocabulary's predicates or relations, each `YIELD` matched to an argument by its label.
- `RETURN` with `AS`, `DISTINCT`, `count`, `min`, `max`, `sum` and `collect`, then `ORDER BY`, `SKIP` and `LIMIT`.
- `toLower`, `toUpper`, `replace`, `split(s, sep)[i]`, and `+` between text, over the standard library's `str.*`.
- A `$parameter`, as a goal variable the host binds through `Program.Bind`, or with `cypher.Params` as a constant written into every rule, which a parameter a `WITH` or `UNION` reads needs, since `Bind` binds only the goal.

## Where the answers differ

A program's answer is a set of rows, so `RETURN` without `DISTINCT` answers each row once, and `count(x)` counts distinct matches of the pattern rather than paths. A variable-length relationship asks what is reachable, so it doesn't keep Cypher's rule that a path uses a relationship once, which can change what a bounded range over a cycle reaches. A property read through `Props` is a match, so a node without it has no row where Cypher would answer `null`. And `collect` answers jaala's `list`, a text, rather than a list. Each of these is a gap below, and they're written down in the package's doc.

## What is refused

A query a program can't express yet is refused with a `*cypher.Error` whose `Gap` names the issue that would let it, as the last example shows, so a model can be told what to write instead and the host knows what it ran into: a relationship as a value ([#279](https://github.com/panyam/jaala/issues/279)), paths as values ([#280](https://github.com/panyam/jaala/issues/280)), shortest paths ([#281](https://github.com/panyam/jaala/issues/281)), `OPTIONAL MATCH` ([#282](https://github.com/panyam/jaala/issues/282)), lists, maps and `UNWIND` ([#283](https://github.com/panyam/jaala/issues/283)), other functions ([#284](https://github.com/panyam/jaala/issues/284)), writes ([#287](https://github.com/panyam/jaala/issues/287)), `USE` ([#289](https://github.com/panyam/jaala/issues/289)), and `CALL { }` subqueries or an `ORDER BY` or `LIMIT` inside a `WITH` or a `UNION` part ([#364](https://github.com/panyam/jaala/issues/364)). A mistake against the graph, such as a label or property it doesn't have, is an `*Error` with no `Gap`, and says what the graph does hold.
