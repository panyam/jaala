---
title: "Arithmetic and units"
description: "Expressions in comparisons, ?y = expr to compute a value, functions that run in any direction, and how units multiply, divide and get their names."
prev: {url: "/jaala/guide/types-and-constants/", title: "Types and constants"}
next: {url: "/jaala/guide/negation/", title: "Negation"}
---

A comparison can compute: `?w * 2 > ?limit`, or `?y = expr`, which gives `?y` the expression's value ([#5](https://github.com/panyam/jaala/issues/5)). These use resistors with a resistance, a current and the rail they sit on:

{{ demo "demos/guide/arithmetic-product.yaml" }}

## Functions run in any direction

Arithmetic is written in functions the standard library registers: `num.add`, `num.sub`, `num.mul`, `num.div` and `num.neg`. Each is a relation of its operands and its result, so `?v = ?i * ?r` is `num.mul(?i, ?r, ?v)`, and it runs from whichever two it has. Given the voltage and the current, the same expression finds the resistance, which is Ohm's law used in either direction:

{{ demo "demos/guide/arithmetic-solve.yaml" }}

Where the expression is written doesn't matter: a step waits until what it needs is bound, as a comparison does. Expressions nest with the usual precedence and parentheses:

{{ demo "demos/guide/arithmetic-parallel.yaml" }}

and can call a function by name, in which case the function's last argument is its result, so `num.div(?r, 2)` is `?r / 2`. A host's own functions work the same way, with `name(args)` calling the relation `name` with one more argument for the result:

{{ demo "demos/guide/arithmetic-function.yaml" }}

A division by zero, or a value that isn't a number, has no result, so that row has none rather than the query failing.

## In the answer and in a rule's head

An expression can be an answer column too, named as it's written without its `?`s, so `?i * ?r` answers a column `i * r` ([#206](https://github.com/panyam/jaala/issues/206)):

{{ demo "demos/guide/arithmetic-projection.yaml" }}

and a rule's head can compute an argument, as `?y = expr` in its body would:

{{ demo "demos/guide/arithmetic-head.yaml" }}

A column like `count(?n)` stays an aggregate, so a host's own function alone in a column is called through an expression around it, or by its dotted name.

## Functions written as rules

A rule can be a function too. `power` below has no relation to read: its body computes `?p` from `?v` and `?i`, which it takes from its caller ([#208](https://github.com/panyam/jaala/issues/208)), so an expression calls it as `power(?v, ?i)` with the result filling its last argument:

{{ demo "demos/guide/arithmetic-rule-function.yaml" }}

The call is written into its caller before the query runs, so `power` runs in any direction `num.mul` does, and called as a relation, `power(?v, ?i, ?w)`, it means the same. Its signature marks as inputs the head's earliest arguments its body can't compute, here `?v` and `?i`, and a call that leaves `num.mul` too little to run from is refused. Like any [rule taking arguments from its caller](/jaala/guide/modules-and-vocabulary/#arguments-from-the-caller), a function has one rule and doesn't reach itself.

## Units

A number can carry a unit, and a query can write one after a number: `1kohm`, `3.3V`, `0.5A`. A prefix is folded in, so `1kohm` is 1000 ohm and compares with a source's ohms:

{{ demo "demos/guide/arithmetic-literal.yaml" }}

A rule's head folds its numbers the same way, so `standard(1kohm)` holds 1000 ohm and joins with a source's ohms ([#224](https://github.com/panyam/jaala/issues/224)):

{{ demo "demos/guide/arithmetic-head-unit.yaml" }}

A product's unit multiplies and a quotient's divides, and the result takes a unit's name when the vocabulary has one: amps times ohms is volts, above. Adding or comparing needs one unit, and a bare number takes the other side's. Volts plus amps has no answer:

{{ demo "demos/guide/arithmetic-unlike.yaml" }}

A unit nobody named is still a unit, written as what it is:

{{ demo "demos/guide/arithmetic-composite.yaml" }}

The standard library names the SI prefixes, the seven SI base units (kg, m, s, A, K, mol, cd), the gram as a thousandth of a kg, and an electrical set (A, V, ohm and Ω, W, J, C, F, H, Hz) defined over the base units, and a host adds its own with `Vocabulary.AddUnit`: a base unit, a prefix, or a name for a product, such as `speed` for `m/s`. A unit's first name is how answers write it. Since V is kg·m²/(A·s³), a unit a host names over kg, m and s meets the electrical ones: with `N` as `kg*m/s^2`, `N*m` is `J`. An unnamed unit is still written in volts where it can be (`V/m`, not `kg*m/(A*s^3)`), since the library writes kg through V (`Vocabulary.WriteBaseThrough`), and mass is in kg, so `5g` is `0.005kg`. A literal's unit is letters only, so a compound one is written as arithmetic, `3m / 1s`.

A prefix moves the decimal point, so `3300mV` is the `3.3V` a reading in volts holds, not the 3.3000000000000003 that multiplying by 0.001 gives.

To jaala a unit is a tag: two numbers order and aggregate together when their units are the same text, and what `mV` means is the vocabulary's. So a source's values are read in the unit it serves them in, and a host keeping lengths in millimetres writes a bare threshold in millimetres ([#303](https://github.com/panyam/jaala/issues/303)):

{{ demo "demos/guide/arithmetic-source-unit-tag.yaml" }}

A host whose source mixes scales for one quantity asks jaala to fold them as it reads them, with `Vocabulary.FoldSourceUnits` ([#205](https://github.com/panyam/jaala/issues/205)). Then `1000mV` from a source is read as `1V`, so it joins, groups, dedups and orders with the source's volts. A number whose text only spelled it reads as the folded number; text saying more, such as `5 mV`, is kept as the data wrote it.

{{ demo "demos/guide/arithmetic-prefixed-source.yaml" }}

A store that looks values up, evaluates a pushed conjunction or filters for such a host holds what it holds, but is asked by the folded value: a lookup for `1V` has to find the `1000mV` it serves.

A unit's scale needn't be a power of ten. A host names durations over seconds, or a binary prefix, with an exact factor, and a query's literals read them:

```go
v.AddUnit("min", "60*s")
v.AddUnit("h", "60*min")
v.AddUnit("B", "")
v.AddUnit("Ki", "1024") // a prefix: 2KiB is 2048 B
```

With those, `build(?b, ?d), ?d > 1min, ?d < 1h` reads `1min` as 60 s and `1h` as 3600 s, and a source serving `1.5` in `min` reads as 90 s once the vocabulary folds a source's units.

## What arithmetic can't do yet

A rule can't compute a value and feed it back into its own recursion, since `n(?m) :- n(?n), ?m = ?n + 1` never stops:

{{ demo "demos/guide/arithmetic-recursion.yaml" }}
