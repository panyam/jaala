---
title: "Asking in Cypher"
description: "Compiling Cypher into a jaala program over a host's graph view, or one a node, edge and attr store writes itself: what it compiles, where its answers differ from a Cypher engine's, and what it refuses with the issue that would let it."
prev: {url: "/jaala/guide/mcp-server/", title: "Serving agents"}
---

Someone who knows a graph query language, or a model that has read a lot of it, can ask in Cypher, and `github.com/panyam/jaala/frontends/cypher` compiles the query into a jaala program the engine answers like any other ([#352](https://github.com/panyam/jaala/issues/352), mission [#356](https://github.com/panyam/jaala/issues/356)). It's a module of its own, with no dependency beyond jaala. The program it writes is ordinary jaala text, so `Validate`, `Eval`, `Explain` and `Locate` read it as they read one written by hand. The compiler is still growing: `WITH`, `UNION`, `CALL` and `EXISTS` and `COUNT` subqueries are #352's next part.

```go
{{ includeFileText "examples/guide/cypher_test.go" }}
```

## The graph view

A relation's schema doesn't say which of its arguments is an endpoint, so the host writes a `cypher.Graph` saying how its relations read as nodes, relationships and properties:

- A `Node` is the relation holding one label's nodes, the argument holding a node's key, and its properties by argument. A node is its key value, so `(p:Package)` binds `p` to a package's name, and `RETURN p` answers it. A property at the key reads the key itself, so `{name: 'app'}` is the node `app`.
- An `Edge` is the relation holding one relationship type, read `From` one argument `To` another, with its own properties.
- `Fixed` pins arguments to constants, for a store that keeps every label in one relation and tells them apart by a value (`node(id, "Function")`), and `Props` names a key-value relation, `attr(node, key, value)`, for a property no `Node` lists.

`Graph.Validate(v)` checks each relation is a member of the vocabulary at an arity holding every argument named, and `Compile` checks it first.

## A node, edge and attr store

Declaire and Agni keep their graphs the same way, every node in `node(id, kind)`, every relationship in `edge(from, kind, to)`, and properties in `attr(id, key, value)`, and writing a `Node` and an `Edge` for each kind by hand gets old quickly. `cypher.Store` names the three relations and `Store.Graph(nodeKinds, edgeKinds)` writes the graph: a label per node kind, keyed by id with the kind fixed, a type per edge kind from its first argument to its third, and `attr` as the property relation ([#366](https://github.com/panyam/jaala/issues/366)). `Store.Kinds(ctx, base)` reads the kinds from the data, for a host whose kinds are its data, as the last example above does. It reads both relations whole, so a host asks once per data version.

A kind becomes a label the way labels are usually written, `func` as `Func` and `http_route` as `HttpRoute` (`cypher.DefaultLabel`), and a type upper case with underscores, `dispatches_to` as `DISPATCHES_TO` (`cypher.DefaultType`). A host with other conventions sets `Store.Label` or `Store.Type`. Two kinds that come out as one label make a graph `Validate` refuses, since a label would mean two things.

The graph is a description of the vocabulary rather than anything Cypher's, so it may move into jaala's core when a second reader arrives, such as a GQL front end or drawing a dataset as a graph.

## What it compiles

`cypher.Compile(v, graph, text)` returns a `cypher.Program`: the program's `Text`, its parsed `Query`, the answer's `Columns` as Cypher names them, and the goal variable each `$parameter` is, which `Program.Bind` turns into a `datalog.Bind` option. Helper relations it writes start with `cypher_`, such as the recursive `cypher_reach1` the first example's variable-length step became.

It compiles the part of Cypher a program expresses today, which is mostly the read-only core: `MATCH` over node and relationship patterns (labels, inline properties, either direction, several types, variable length as recursion or, when bounded, as the paths of each length up to 8 hops), `WHERE` (comparisons, `AND`, `OR`, `XOR`, `NOT`, `IS NULL`, `STARTS WITH`, `ENDS WITH`, `CONTAINS`, `=~`, `IN` over a written list, a label test, a pattern), and `RETURN` with aliases, `DISTINCT`, `count`, `min`, `max`, `sum` and `collect`, then `ORDER BY`, `SKIP` and `LIMIT`.

## Where its answers differ

We'd rather say where a program's answer can't be Cypher's than have the compiler guess, so here they are:

- The rows are distinct, as a program's answer is: `RETURN` without `DISTINCT` answers each row once, and `count(x)` counts distinct bindings of the match, not paths. Cypher's duplicate rows need a relationship as a value ([#279](https://github.com/panyam/jaala/issues/279)).
- A property read through the graph's `Props` relation is a match: a node without it has no row, where Cypher would answer null ([#282](https://github.com/panyam/jaala/issues/282)).
- A variable-length match is reachability: it doesn't keep Cypher's rule that a path uses a relationship once, which can differ for a bounded range over a cycle ([#280](https://github.com/panyam/jaala/issues/280)).
- `collect` answers a text listing the values, not a list ([#283](https://github.com/panyam/jaala/issues/283)).

## What it refuses

The rest it refuses with a `*cypher.Error` carrying the `Pos` (line, column and byte offset) in the Cypher text, and, for Cypher a program can't express yet, the `Gap`: the jaala issue that would let it, with a `Capability` in words a model can act on, as the last example shows. Those are a relationship variable read anywhere ([#279](https://github.com/panyam/jaala/issues/279)), paths as values ([#280](https://github.com/panyam/jaala/issues/280)), shortest paths and long or zero-length bounds ([#281](https://github.com/panyam/jaala/issues/281)), `OPTIONAL MATCH` ([#282](https://github.com/panyam/jaala/issues/282)), lists, maps and `UNWIND` ([#283](https://github.com/panyam/jaala/issues/283)), functions beyond the aggregates above ([#284](https://github.com/panyam/jaala/issues/284)), writes ([#287](https://github.com/panyam/jaala/issues/287)) and `USE` ([#289](https://github.com/panyam/jaala/issues/289)). A `Gap` of 0 is a mistake in the text or against the graph, such as a label it doesn't have.
