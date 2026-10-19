---
title: "Syntax"
description: "Every form a query can take: terms, literals, comparisons, rules, the goal and its projection, and comments."
next: {url: "/jaala/guide/types-and-constants/", title: "Types and constants"}
---

A query is zero or more rules, each ending in `;`, followed by one goal. Whitespace doesn't matter, so you can write a query on one line or many, and a `#` outside a string starts a comment that runs to the end of the line.

## Terms

A term is an argument of a literal, and there are four kinds:

- a variable, `?p`, which takes whatever value makes the goal hold;
- the wildcard `_`, which matches anything and binds nothing, so two `_` are never the same value;
- a string in double quotes, `"api"`, which takes no escapes: what's between the quotes is the value as written, and a quote inside it follows a backslash, which stays too (`"\"x\""` is the five characters `\"x\"`, which a regex reads as `"x"`);
- a number, `300` or `-1.5`.

{{ demo "demos/guide/syntax-number.yaml" }}

A variable that appears twice in a goal has to take the same value both times, which is how a goal joins. This finds pairs of packages that import each other:

{{ demo "demos/guide/syntax-shared-variable.yaml" }}

## Literals

A goal or a rule body is a list of literals separated by commas, and all of them have to hold. A literal is one of three things:

- an atom, `imports(?p, ?q)`, which matches a relation's tuples;
- a negated atom, `not test(_, ?p)`, covered on the [negation]({{.Site.PathPrefix}}/guide/negation/) page;
- a comparison between two terms, with `=`, `!=`, `<`, `<=`, `>` or `>=`.

{{ demo "demos/guide/syntax-comparisons.yaml" }}

A comparison only compares, which is a bit different from `=` in most programming languages. `=` checks that two values are equal, and it doesn't give a value to a variable that has none, so a variable a comparison uses has to appear in an atom of the same goal or rule. Where the comparison is written doesn't matter, since it's checked once the atom has run:

{{ demo "demos/guide/syntax-equals-compares.yaml" }}

An atom can also name the fact it matched: `imports(?a, ?b) as ?r` binds `?r` to that fact, so a query can return it, compare two with `!=`, or join on it ([#279](https://github.com/panyam/jaala/issues/279)). This is what Cypher's `-[r:IMPORTS]->` binds. A fact's name is the id its store gave it, or, when the store gave none, as here, the relation and its values written the way a query writes them:

{{ demo "demos/guide/syntax-fact.yaml" }}

Two facts are equal only when they're the same fact, so `?r != ?s` keeps a path from using one fact twice, which is the rule a Cypher match follows on its own. Here it's what tells the import cycle between `cache` and `config` apart from a package importing itself, which the data doesn't have:

{{ demo "demos/guide/syntax-fact-differs.yaml" }}

Only a positive atom over a base relation can name its fact. A derived relation's tuples, a predicate's matches and a negated atom's absence aren't facts any store holds, so `as` there is refused. A store keeping more about a fact (where an edge came from, what it costs) keys it by the fact's id, and a query reads that through an ordinary relation of the host's: `imports(?a, ?b) as ?r, imports_attr(?r, "kind", ?k)`. The [stores]({{.Site.PathPrefix}}/guide/stores/) page says how a host gives its facts ids.

## Walking a relation

`graph.closure("imports", ?from, ?to, min, max, ?hops)` holds when `?to` is reachable from `?from` in `min` to `max` steps of a relation of two arguments, and `?hops` is the fewest it takes ([#281](https://github.com/panyam/jaala/issues/281)). It's the recursive rule `reach(?a, ?c) :- reach(?a, ?b), imports(?b, ?c)` with a bound and a count the rule can't carry, since a rule computing a number it then reads again might never finish. Max `0` is no bound. What `app` reaches within two steps:

{{ demo "demos/guide/syntax-closure.yaml" }}

It walks from whichever end the body binds, so asking what reaches `util` walks back from it. Each answer cites one path of `?hops` steps, in order from `?from` to `?to`, and with `Witnesses()` its witness has a node per step:

{{ demo "demos/guide/syntax-closure-back.yaml" }}

A node reaches itself only through a cycle, at the cycle's length, as the recursive rule's would, and min `0` makes every node the relation holds reach itself in none (a value it doesn't hold reaches nothing, whichever end is bound):

{{ demo "demos/guide/syntax-closure-cycle.yaml" }}

The walk is breadth first, once per node (and per depth below `min`), so it costs what lies within the bound, where a rule counting depth would derive every walk of every length. The relation can be base or derived, by its full name (`"deps.depends_on"`), but it must be complete before the walk reads it, so a relation can't walk itself, and one whose rules each read one base relation, such as `calls(?a, ?b) :- edge(?a, "CALLS", ?b)`, is walked through that relation's facts directly. `graph.closure` is the engine's name, which a vocabulary can't register.

## Every path

`graph.closure` answers whether a node is reachable and how far. `graph.paths("imports", ?from, ?to, mode, min, max, ?path)` answers with each path itself, bound to `?path` as a value ([#280](https://github.com/panyam/jaala/issues/280)). Every way `db` reaches `util` without passing a package twice:

{{ demo "demos/guide/syntax-paths.yaml" }}

A path is a JSON list of its nodes, so an answer shows it as it is, and two paths are the same value when they pass the same nodes. The mode says how a path may repeat. `"acyclic"` passes no node twice, `"simple"` the same but may end where it started, closing a cycle (GQL's `SIMPLE`), `"trail"` takes no step twice (Cypher's rule), and `"walk"` repeats anything, so it needs a `max`. Through the `cache` and `config` cycle a trail goes round once, which an acyclic path can't:

{{ demo "demos/guide/syntax-paths-modes.yaml" }}

A simple path from a package back to itself is a cycle through it, each other package passed once:

{{ demo "demos/guide/syntax-paths-simple.yaml" }}

`"shortest"` gives one path of the fewest steps for each pair, the one whose nodes come first, step by step, so it's the same path whichever end the body binds. With `min` and `max` both 2, it's the packages `app` reaches in exactly two steps and how:

{{ demo "demos/guide/syntax-paths-shortest.yaml" }}

Over a relation of three arguments, from, edge and to, a path names the edges it took as well, and a trail then takes each edge once, whichever way it runs. A fact's id (`as ?r`, [above](#terms)) makes a good edge:

{{ demo "demos/guide/syntax-paths-edges.yaml" }}

The `path.*` functions read a path ([reference]({{.Site.PathPrefix}}/reference/builtins/#paths)): its length, its nodes and steps by index, and its edges. Each answer cites its path's facts in order, and its witness has a node per step, as a closure's does. The walk is depth first and reads each node's steps once, but paths multiply around cycles, so a budget or a `limit` is worth having on a dense relation: the walk stops as soon as the query has enough. `graph.paths` is the engine's name too.

## The goal and its projection

After the goal, `=>` and a list of columns say what the answer holds. The columns are variables, [aggregates]({{.Site.PathPrefix}}/guide/aggregation/) or [arithmetic]({{.Site.PathPrefix}}/guide/arithmetic/#in-the-answer-and-in-a-rules-head), and after them can come `having`, `order by`, `limit` and `offset`, in that order. Leave out `=>` and its columns, and the answer has one column per variable of the goal, in the order they first appear:

{{ demo "demos/guide/syntax-no-projection.yaml" }}

## Rules

A rule is a head, `:-`, and a body: `depends_on(?a, ?b) :- imports(?a, ?b);`. Each argument of the head is a variable the body binds, a constant, an [aggregate]({{.Site.PathPrefix}}/guide/aggregation/#an-aggregate-in-a-rule), or [arithmetic]({{.Site.PathPrefix}}/guide/arithmetic/#in-the-answer-and-in-a-rules-head) over what the body binds, and a variable can declare its type, as in `size(?p, ?n: number)`, which the [next page]({{.Site.PathPrefix}}/guide/types-and-constants/) covers. A relation can have several rules, and its tuples are everything any of them derives. A relation name can contain dots and hyphens, which is how jaala names the standard library's tests (`str.contains`) and a module's members (`net.has_test_point`).

## Endings from other Datalogs

jaala also reads the endings Datalog, Prolog and Soufflé write, so a program written in their habits runs as its jaala spelling does. A `.` followed by a space or the end ends a clause, as `;` does; a `.` inside `str.suffix` or `1.5` doesn't. A goal written `?- r(?a)` or `=> r(?a)` asks `r(?a)` with no projection, and `!atom` is `not atom`:

{{ demo "demos/guide/syntax-other-endings.yaml" }}

A variable is still `?a`. A capitalised word, as Prolog writes a variable, is refused rather than read as one, because `pin(?r, GND)` meant as a constant would otherwise match every net.
