---
title: "Asking in Cypher"
description: "Compiling Cypher into a jaala program over a host's graph view, or one a node, edge and attr store writes itself: what it compiles, where its answers differ from a Cypher engine's, and what it refuses with the issue that would let it."
prev: {url: "/jaala/guide/mcp-server/", title: "Serving agents"}
---

Someone who knows a graph query language, or a model that has read a lot of it, can ask in Cypher, and `github.com/panyam/jaala/frontends/cypher` compiles the query into a jaala program the engine answers like any other ([#352](https://github.com/panyam/jaala/issues/352), mission [#356](https://github.com/panyam/jaala/issues/356)). It's a module of its own, with no dependency beyond jaala. The program it writes is ordinary jaala text, so `Validate`, `Eval`, `Explain` and `Locate` read it as they read one written by hand.

```go
{{ includeFileText "examples/guide/cypher_test.go" }}
```

## The graph view

A relation's schema doesn't say which of its arguments is an endpoint, so the host writes a `cypher.Graph` saying how its relations read as nodes, relationships and properties:

- A `Node` is the relation holding one label's nodes, the argument holding a node's key, and its properties by argument. A node is its key value, so `(p:Package)` binds `p` to a package's name, and `RETURN p` answers it. A property at the key reads the key itself, so `{name: 'app'}` is the node `app`.
- An `Edge` is the relation holding one relationship type, read `From` one argument `To` another, with its own properties.
- `Fixed` pins arguments to constants, for a store that keeps every label in one relation and tells them apart by a value (`node(id, "Function")`), and `Props` names a key-value relation, `attr(node, key, value)`, for a property no `Node` lists.
- `Derived` names, per property, a relation or predicate holding it for any node, `name.of(node, value)`, for one the host computes rather than stores, such as a function's short name worked out from its id ([#378](https://github.com/panyam/jaala/issues/378)). Each read is an atom over it, `{name: 'Foo'}` as `name.of(?f, "Foo")` and `RETURN f.name` as `name.of(?f, ?v)`, so the predicate's modes say which the host answers. A label's own column wins over it, and it wins over `Props`.

`Graph.Validate(v)` checks each relation is a member of the vocabulary at an arity holding every argument named, and `Compile` checks it first.

## A node, edge and attr store

Declaire and Agni keep their graphs the same way, every node in `node(id, kind)`, every relationship in `edge(from, kind, to)`, and properties in `attr(id, key, value)`, and writing a `Node` and an `Edge` for each kind by hand gets old quickly. `cypher.Store` names the three relations and `Store.Graph(nodeKinds, edgeKinds)` writes the graph: a label per node kind, keyed by id with the kind fixed, a type per edge kind from its first argument to its third, and `attr` as the property relation ([#366](https://github.com/panyam/jaala/issues/366)), with `Store.Derived` as the graph's `Derived`. `Store.Kinds(ctx, base)` reads the kinds from the data, for a host whose kinds are its data, as the last example above does. It reads both relations whole, so a host asks once per data version.

A kind becomes a label the way labels are usually written, `func` as `Func` and `http_route` as `HttpRoute` (`cypher.DefaultLabel`), and a type upper case with underscores, `dispatches_to` as `DISPATCHES_TO` (`cypher.DefaultType`). A host with other conventions sets `Store.Label` or `Store.Type`. Two kinds that come out as one label make a graph `Validate` refuses, since a label would mean two things.

The graph is a description of the vocabulary rather than anything Cypher's, so it may move into jaala's core when a second reader arrives, such as a GQL front end or drawing a dataset as a graph.

## What it compiles

`cypher.Compile(v, graph, text)` returns a `cypher.Program`: the program's `Text`, its parsed `Query`, the answer's `Columns` as Cypher names them, and the goal variable each `$parameter` is, which `Program.Bind` turns into a `datalog.Bind` option. Helper relations it writes start with `cypher_`: the recursive `cypher_reach1` the first example's variable-length step became, a `cypher_with` rule per `WITH` stage, a `cypher_match` rule per alternative of an `OR`, a `cypher_exists` rule for a pattern or `EXISTS { }` in `WHERE`, and a `cypher_union` relation for a `UNION`.

A `Bind` reaches only the goal, and a rule's variables are its own, so a `$parameter` a rule reads (one inside a `WITH` stage, a `UNION` part, an `OR`, a pattern test or a variable-length step) is refused unless the host compiles with `cypher.Params(values)`, which writes each value in as a constant, as the `WITH` example's second program shows. That's one compile per set of values, which suits a translator compiling each question once.

It compiles the part of Cypher a program expresses today, which is mostly the read-only core: `MATCH` over node and relationship patterns (labels, inline properties, either direction, several types, variable length as recursion or, when bounded, as the paths of each length up to 8 hops), `WHERE` (comparisons, `AND`, `OR`, `XOR`, `NOT`, `IS NULL`, `STARTS WITH`, `ENDS WITH`, `CONTAINS`, `=~`, `IN` over a written list, a label test, a pattern, `EXISTS { }` and `COUNT { }`, the last a body aggregate so a node with no match counts 0), and `RETURN` with aliases, `DISTINCT`, `count`, `min`, `max`, `sum` and `collect`, then `ORDER BY`, `SKIP` and `LIMIT`.

Past one `MATCH` and `RETURN`, `WITH` closes a stage with its aggregates and its `WHERE` (Cypher's way of filtering on a count), and a node it hands on keeps its labels, so a property read after it still finds its column. `UNION` and `UNION ALL` join parts that return the same columns. `CALL proc(args) YIELD name, ...` reads one of the vocabulary's predicates or relations, each `YIELD` landing on the argument its label names, so `CALL str.split(p.license, '-') YIELD index, part` is `str.split`. And `toLower`, `toUpper`, `replace`, `split(s, sep)[i]` and `+` between text compile to the standard library's `str.*`; `+` joins when a side is a string, a string function or a property the schema types as text, and adds otherwise.

## Where its answers differ

We'd rather say where a program's answer can't be Cypher's than have the compiler guess, so here they are:

- The rows are distinct, as a program's answer is: `RETURN` without `DISTINCT` answers each row once, and `count(x)` counts distinct bindings of the match, not paths. Cypher's duplicate rows need a relationship as a value ([#279](https://github.com/panyam/jaala/issues/279)).
- A property read through the graph's `Props` relation, or a `Derived` one, is a match: a node without it has no row, where Cypher would answer null ([#282](https://github.com/panyam/jaala/issues/282)).
- A variable-length match is reachability: it doesn't keep Cypher's rule that a path uses a relationship once, which can differ for a bounded range over a cycle ([#280](https://github.com/panyam/jaala/issues/280)).
- `collect` answers a text listing the values, not a list ([#283](https://github.com/panyam/jaala/issues/283)).

## What it refuses

The rest it refuses with a `*cypher.Error` carrying the `Pos` (line, column and byte offset) in the Cypher text, and, for Cypher a program can't express yet, the `Gap`: the jaala issue that would let it, with a `Capability` in words a model can act on, as the last example shows. Those are a relationship variable read anywhere ([#279](https://github.com/panyam/jaala/issues/279)), paths as values ([#280](https://github.com/panyam/jaala/issues/280)), shortest paths and long or zero-length bounds ([#281](https://github.com/panyam/jaala/issues/281)), `OPTIONAL MATCH` ([#282](https://github.com/panyam/jaala/issues/282)), lists, maps and `UNWIND` ([#283](https://github.com/panyam/jaala/issues/283)), functions beyond the aggregates above ([#284](https://github.com/panyam/jaala/issues/284)), writes ([#287](https://github.com/panyam/jaala/issues/287)), `USE` and `LOAD CSV` ([#289](https://github.com/panyam/jaala/issues/289)), `CALL { }` subqueries, an `ORDER BY`, `SKIP` or `LIMIT` inside a `WITH` or a `UNION` part, a label expression with `|`, `!` or `%`, and a query with no `MATCH` before its `RETURN` or no `RETURN` at all ([#364](https://github.com/panyam/jaala/issues/364)), since a rule holds a relation, with no order or limit to keep, and a `WHERE` that expands to more than 16 alternatives, such as a chain of `XOR`s ([#388](https://github.com/panyam/jaala/issues/388)). A `Gap` of 0 is a mistake in the text or against the graph, such as a label it doesn't have, or what isn't a query (`SHOW`, `CREATE INDEX`, `EXPLAIN`).

The parser reads all of Neo4j 5's query syntax ([#382](https://github.com/panyam/jaala/issues/382)), comprehensions, `CASE`, quantified paths, `IS ::` and the writing clauses included, so valid Cypher is never refused as a typo. It reports the first refusal only once the whole text has parsed: a mistake anywhere in the query is reported before a gap, so a model fixing the gap doesn't meet the typo on its next try. A `WHERE` inside a node or relationship of a `MATCH` (`(p:Package WHERE p.license = 'MIT')`), `IS Label`, `A&B` labels, hex and octal integers, `OFFSET`, `RETURN *, x` and a `CYPHER` or `PROFILE` prefix compile.

## A model writing Cypher

`cypher.NewFrontEnd(graph, examples...)` is the front end `nl.Translate` takes in `Options.FrontEnd`, so a host's model writes Cypher instead of jaala's syntax ([#354](https://github.com/panyam/jaala/issues/354), and [front ends]({{.Site.PathPrefix}}/guide/plain-language/#front-ends) on the plain-language page). Its card lists the graph's labels with their properties and its relationship types, one a line, and its instructions say what of Cypher compiles and what doesn't. Its examples are questions with the Cypher answering them, which `Translate` compiles and checks before it asks the model anything.

`cypher.Grammar(graph)` holds a model to the subset as GBNF, which llama.cpp decodes under: `MATCH`, `WHERE`, `WITH`, `UNION`, `EXISTS { }`, and `RETURN` with its aggregates, `ORDER BY`, `SKIP` and `LIMIT`. Its only names are the graph's labels, types and properties. It leaves out `CALL`, `$parameters` and everything refused below. Every text it accepts parses. Whether the query means something, such as returning a variable it matched, is still `Compile`'s to say, as a program's is `Validate`'s.

Each refusal goes back as `Explain` writes it, a mistyped label with the label it likely meant, and a construct jaala can't express yet as the kind `gap`, so a report counts which gaps real questions hit. The last example above is a model mistyping a label and fixing it on its second try.

## Errors in the query's terms

A query can compile and still fail when the program checks or runs: a value an argument's type or closed domain refuses (`{tier: 'middle'}`), or a number compared with text (`p.loc > 'big'`). The engine reports those against the program, so `Program.Locate(err)` maps one back to the Cypher that wrote the failing piece: the node pattern, the relationship, the `WHERE` condition, the property or the `RETURN` item ([#353](https://github.com/panyam/jaala/issues/353)). The compiler records where each piece of the program came from as it writes it, and the engine says which argument or comparison a type error is about, so a refused value in the second of two patterns is placed at that pattern, not at the first. `cypher.Explain(query, program, err)` writes any error, a compile refusal included, the way a model or an agent fixes the query from: the message with the program's variables named as the query names them (`p.loc`, not `?p_loc`), the query's line with a caret under the place, the fix when the engine has one, and for a gap, that the query should do without it, as the last example prints. An error with no place, such as a spent budget, is explained without a caret.
