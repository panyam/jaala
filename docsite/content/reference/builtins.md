---
title: "Built-ins"
description: "Everything the standard library adds to a vocabulary: string tests, edit distance, building text, a number's value and unit, arithmetic, and absent, read from its registry."
---

`stdlib.Register` adds these to a vocabulary. The table is built from the registry itself when the site is built, with each name's signature and the doc it was registered with.

{{ builtins }}

All but `str.distance`, the text functions, `num.value`, `num.unit` and the arithmetic functions are tests: each holds or it doesn't, and binds nothing. So every argument needs a value from the rest of the goal, pretty much the way a comparison does. jaala's planner runs a test once its arguments are bound, wherever you wrote it in the goal, and refuses one whose arguments nothing binds:

{{ demo "demos/reference/builtins-unbound.yaml" }}

## String tests

`str.contains`, `str.prefix` and `str.suffix` compare plain substrings:

{{ demo "demos/reference/builtins-contains.yaml" }}

{{ demo "demos/reference/builtins-prefix.yaml" }}

{{ demo "demos/reference/builtins-suffix.yaml" }}

`str.glob` matches the whole string against a SQLite-style glob, where `*` is any run of characters, `?` one character, and `[a-c]` one of a class:

{{ demo "demos/reference/builtins-glob.yaml" }}

`str.match` takes a regular expression in Go's syntax, unanchored, so anchor it with `^` and `$` to match the whole string:

{{ demo "demos/reference/builtins-match.yaml" }}

A pattern that doesn't compile is refused before anything runs, with the compiler's reason:

{{ demo "demos/reference/builtins-bad-regex.yaml" }}

{{ demo "demos/reference/builtins-bad-glob.yaml" }}

Go code that has to agree with these (a host filtering the same names outside a query, say) can use `stdlib.CompileGlob` and `stdlib.CompilePattern`, the compilers the tests use.

## Edit distance

`str.distance(?a, ?b, ?d)` binds `?d` to the edit distance between two strings: the fewest characters inserted, deleted or replaced to turn one into the other (Levenshtein's distance). A character is a Unicode character, so `Ω` against `O` is one edit. Both strings need values from the rest of the goal, and `?d` can be a variable it binds or a number it tests. Two names a character apart are often one name misspelled, so this finds them:

{{ demo "demos/reference/builtins-distance.yaml" }}

Each pair is measured once per binding, so a goal comparing every name with every other does work for every pair. Matching is exact on case: `EN` and `en` are two edits apart.

With neither string bound there is nothing to measure, and the goal is refused:

{{ demo "demos/reference/builtins-distance-unbound.yaml" }}

## Building text

`str.concat`, `str.replace`, `str.lower`, `str.upper`, `str.capitalize` and `str.split` build text from text, so a name one fact spells can be matched to the name another fact spells differently. Each takes its inputs and then its result, like the arithmetic functions, so a call can sit in a body or in an expression (`?f = str.concat(?p, ".go")`). The inputs need values from the rest of the goal, and with the result given too a call tests it.

`str.concat(?head, ?tail, ?text)` puts two strings together:

{{ demo "demos/reference/builtins-concat.yaml" }}

Like `num.add` it runs from any two of its three values, so given the text and one end it cuts that end off. Text that doesn't start or end with it gives no row, so this finds each package that has a test named after it, and leaves out `auth_fuzz`:

{{ demo "demos/reference/builtins-concat-cut.yaml" }}

`str.replace(?s, ?old, ?new, ?out)` replaces every occurrence:

{{ demo "demos/reference/builtins-replace.yaml" }}

`str.lower` and `str.upper` change the case of every character, and `str.capitalize` only the first:

{{ demo "demos/reference/builtins-case.yaml" }}

`str.split(?s, ?sep, ?i, ?part)` gives one row per part between separators, numbered from 0:

{{ demo "demos/reference/builtins-split.yaml" }}

Given an index it gives that part alone, and a negative index counts from the end: -1 is the last part, which is a qualified name's own name, and -2 the one before it. An index past either end gives no row:

{{ demo "demos/reference/builtins-split-last.yaml" }}

None of them makes text up from nothing, so a call with nothing to read is refused:

{{ demo "demos/reference/builtins-text-unbound.yaml" }}

## Numbers and units

A number can carry a unit (`5V`, `0.5A`), which a host's source sets. `num.value(?x, ?n)` binds `?n` to `?x`'s number without its unit, and `num.unit(?x, ?u)` binds `?u` to the unit, or to `absent` for a number that has none. Both need `?x` from the rest of the goal, and bind nothing for a value that isn't a number:

{{ demo "demos/reference/builtins-num.yaml" }}

They're how a query says what it wants when the units in a column differ, which an aggregate won't guess at ([guide]({{.Site.PathPrefix}}/guide/aggregation/#units)).

## Arithmetic

`num.add`, `num.sub`, `num.mul`, `num.div` and `num.neg` are the functions arithmetic is written in: `?v = ?i * ?r` is `num.mul(?i, ?r, ?v)`. Called as relations, each takes its operands and then its result:

{{ demo "demos/reference/builtins-arith.yaml" }}

Each runs from any two of its three values (one of two for `num.neg`), so the same call finds a product or a missing factor. Here the factor that makes `app`'s 300 lines three times something:

{{ demo "demos/reference/builtins-arith-inverse.yaml" }}

Each carries units through the vocabulary's table. The [guide]({{.Site.PathPrefix}}/guide/arithmetic/) shows them in expressions.

## `absent`

`absent(?x)` holds when `?x` is a field its source didn't state. None of `deps`'s fields are absent, so here it holds for nothing, and the [guide]({{.Site.PathPrefix}}/guide/absent-values/) has an example over facts that do leave fields out:

{{ demo "demos/reference/builtins-absent.yaml" }}

## `coalesce`

`coalesce(?x, ?fallback, ?v)` binds `?v` to `?x`, or to `?fallback` when `?x` is absent. Both must be bound first, absent counting as a value. An expression can call it, `?v = coalesce(?x, 0)`, and it nests for more fallbacks: `coalesce(?a, coalesce(?b, ?c))`. No package has more than 5000 lines, so `big`'s min is absent and the fallback answers:

{{ demo "demos/reference/builtins-coalesce.yaml" }}

## Paths

A path from [`graph.paths`]({{.Site.PathPrefix}}/guide/syntax/#every-path) is text, a JSON list of its nodes, or `{"nodes": [...], "edges": [...]}` over a relation that names its edges. The `path.*` functions read it, each needing the path bound. `path.length(?p, ?n)` is how many steps it takes, and `path.node(?p, ?i, ?node)` gives each node with its index from 0, so `not path.node(?p, _, "config")` keeps the paths that avoid `config`:

{{ demo "demos/reference/builtins-path.yaml" }}

Given an index, `path.node` gives that node alone. `path.nodes(?p, ?l)` is the nodes as a list, the path itself when it names no edges:

{{ demo "demos/reference/builtins-path-node.yaml" }}

`path.step(?p, ?i, ?from, ?to)` gives each step with the nodes it joins, which is how a path of nodes finds its edges again, by joining each step back to the relation it walked:

{{ demo "demos/reference/builtins-path-step.yaml" }}

`path.edge(?p, ?i, ?e)` and `path.edges(?p, ?l)` read a path that names its edges, one over a relation of three arguments, and give nothing for one that doesn't:

{{ demo "demos/reference/builtins-path-edge.yaml" }}

A value that isn't a path gives no row rather than an error, since which values reach a function depends on how the query is planned. They live under `path`, so in a host's own module named `path`, a rule's bare `node(...)` or `edge(...)` means these functions, and the module has to write the vocabulary's relation another way or take another name.

## Lists

A list is a value ([#283](https://github.com/panyam/jaala/issues/283)): the [`list` aggregate]({{.Site.PathPrefix}}/guide/aggregation/) builds one, a rule hands it on, and a path's nodes are one too. It's carried as a JSON list, `["cache","db"]`, with a number in a unit written `{"num": 3.3, "unit": "V"}` so it reads back as that number. A Source may serve one in a column it declares `list`. The `list.*` functions read it, each needing the list bound. `list.member(?l, ?i, ?x)` gives each value with its index from 0, which turns a list back into rows:

{{ demo "demos/reference/builtins-list.yaml" }}

With the value bound, `list.member` is a test that the list holds it. `list.length(?l, ?n)` is how many values it holds, and `list.at(?l, ?i, ?x)` the one at an index, a negative index counting from the end:

{{ demo "demos/reference/builtins-list-at.yaml" }}

A value that isn't a list gives no row rather than an error, as with paths. Two lists are equal when they hold the same values in the same order, and they order between themselves by their text, not value by value. A rule can't build a list other than through the aggregate, which keeps a program finite ([#1](https://github.com/panyam/jaala/issues/1)). Lists live under `list`, so a host's module named `list` has the same caveat as one named `path`.
