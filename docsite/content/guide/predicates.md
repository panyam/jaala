---
title: "Predicates and generators"
description: "A host's own Go inside a query: a filter that tests bound values, and a generator that produces them, with the modes it runs under, its types, and when its answers can be kept."
prev: {url: "/jaala/guide/host-options/", title: "Host options"}
next: {url: "/jaala/guide/stores/", title: "Facts from stores"}
---

Not everything a host knows is a fact it wants to serve. A license list, a parser, a geometry index or a walk over a graph the host already holds in memory is easier to compute than to project into relations. `Vocabulary.AddPredicate` registers a computed predicate at a path, and a query calls it like a relation: `lic.permissive(?l)`, `size.lines(?d, ?n)`. It comes in two kinds. A filter tests values that are all bound, and a generator produces values. A generator is also how a host makes a query fast where jaala's own evaluation would be slow, since it's the host's Go running inside the query, citing what it read ([#41](https://github.com/panyam/jaala/issues/41)).

The examples register one of each beside the guide's `deps` vocabulary. `stdlib`'s `str.*` and `num.*` are registered the same way, and the [built-ins]({{.Site.PathPrefix}}/reference/builtins/) page lists them.

```go
{{ includeFileText "examples/guide/predicates_test.go" }}
```

## Filters

A filter's `ns.Builtin` sets `Holds`, which gets the argument values and says whether the binding passes. Every argument has to be bound when it runs. jaala holds a filter until the literals binding its arguments have run, so one written before them still works, and refuses a body where nothing ever binds one: `query: lic.permissive needs all arguments bound (a variable must appear in a relation before lic.permissive tests it)`.

`not lic.permissive(?l)` runs the same test and keeps the binding when it fails, so a filter and its negation can't disagree. `Example_filter` lists the packages with a permissive license, then the rest: `api`, whose license the source didn't state, fails the test, so it's among the rest. A filter cites nothing, so a row's citations are its facts'. A store that applies a filter exactly as the vocabulary does can be sent it with a pushed join (`ns.FilterSource`, on [facts from stores]({{.Site.PathPrefix}}/guide/stores/#a-store-that-joins)).

## Generators

A generator sets `Gen`. It gets the query's context, the source the query runs over, and one `ns.Arg` per argument, `Value` set where the binding fixes it and `Bound` saying whether it does. It calls `emit` once per solution, with a value for every argument and the citations that justify it, and jaala unifies each solution with the call, so one disagreeing with a bound argument is dropped there. jaala copies what it needs before `emit` returns, so a generator can reuse one buffer for every solution.

`emit` returns an error when the query stops, cancelled or over its `Budget` (every emitted solution counts as work), and the generator returns it as is. A long walk should also check `ctx` itself, and can charge its own steps to the query's budget through `ns.AllowanceFrom(ctx)` ([#361](https://github.com/panyam/jaala/issues/361)): `Spend(n)` errs once the budget is spent, and the generator returns that error as it is, so a walk of a hundred thousand steps under a budget of a thousand stops after a thousand rather than after it's done. `Left()` says what's left, and whether there's a budget at all. What a generator spends counts against the `Budget` beside the query's work but isn't part of `Work`; `Explain` reports it as `Report.Spent`, and on the literal that called the generator. A generator's values must come from finite data the host holds, since jaala's guarantee that evaluation ends rests on no rule inventing a value.

### Modes

`Modes` says which bindings a generator runs under: one list per mode, true where that argument must be bound. `size.lines` runs with its package bound, `{true, false}`, or with nothing bound, `{false, false}`, listing every package. A generator that can list everything says so with an all-false mode, so a full scan is a declaration rather than an accident, and `AddPredicate` refuses a generator with no modes (`Example_generatorNeedsModes`).

The planned `SemiNaive` runs a generator as soon as one of its modes is satisfied, so a host never has to write a body generator-first ([#36](https://github.com/panyam/jaala/issues/36)). In `Example_generator`, `size.lines(?d, ?n)` is written first, but `depends_on` binds `?d`, so the generator is asked about each dependency rather than listing every package, and each row cites `sloc:` for its size. A call with nothing bound lists everything, so it ranks after relations the engine already holds or derives ([#293](https://github.com/panyam/jaala/issues/293)), and a variable only the all-free mode would take counts as unbound when ranking ([#304](https://github.com/panyam/jaala/issues/304)). A body that can never satisfy any mode is refused before anything runs, by every evaluator and by `Validate`: `query: … calls …, and nothing binds what … needs first: it needs …`.

`Naive` doesn't plan. It calls a generator where it's written, with the unbound arguments `Bound: false`, so a generator must refuse a call it can't run, returning an error, rather than read the zero value. `stdlib`'s `str.distance` errs that way.

### Labels and types

`Labels` and `Types` describe the arguments as a base relation's schema does. jaala reads an emitted value as its argument's type, so `size.lines` emitting `ns.N(900)` into a number column compares as a number in `?n > 100`, and a generator reading a number argument waits for the relations binding it, so it reads the spelling they agree on ([#244](https://github.com/panyam/jaala/issues/244)). A generator typing nothing, as most hosts' do, is never held. `Doc` is the line a host shows when it lists the vocabulary.

## When answers can be kept

A `Base` keeps the derived relations a query evaluated in full and reuses them in later queries ([host options]({{.Site.PathPrefix}}/guide/host-options/#sharing-a-base)), which assumes a predicate answers the same for the same arguments and source. One that doesn't, because it reads the clock, the network, or a host index updated in place, sets `Volatile`, and no relation reading it, directly or through others, is kept.

Under `At(v)` a generator's context carries the version (`ns.VersionOf`), so one reading versioned data of its own reads the same version. Under `Across` a generator holds in every version, since jaala can't know what it read ([versions]({{.Site.PathPrefix}}/guide/versions/)).
