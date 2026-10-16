---
title: "Modules and the vocabulary"
description: "How a host names everything a query can call: modules of rules by path, private members, docs and signatures, rules that take an argument from their caller, and the errors Check reports."
prev: {url: "/jaala/guide/stores/", title: "Facts from stores"}
next: {url: "/jaala/guide/absent-values/", title: "Absent values"}
---

The vocabulary is the tree of names a query can call. Its leaves are the relations a source serves, the predicates a host computes (like `str.contains` from the standard library), and the members of modules, which are rules a host ships as a library. A query naming `deps.depends_on` pulls in the module that defines it, and whatever that module reads, so a library can be fairly big without every query paying for all of it.

These examples use the vocabulary from the [host options]({{.Site.PathPrefix}}/guide/host-options/) page, whose `deps` module defines `depends_on` with the help of a private `_step`.

```go
{{ includeFileText "examples/guide/modules_test.go" }}
```

## Paths and private members

A module lives at a path, and its members are named under it: `deps.depends_on`. Inside a module's own rules a bare name means that module's member first and the root's second, so rules don't repeat their module's path. A member whose name starts with `_` is private. The module's rules can call it, while a query can't, and `Lookup` leaves it out of the module's members, so a library can keep its helpers to itself.

## Docs and signatures

The comment lines above a member's first rule are its doc, and `Lookup` returns it with the member's signature and the origin the host gave when adding the module, usually a file name. A signature lists the member's arguments with their types, declared in a rule head or inferred through its rules, which is what a host shows when it lists what's available to query.

## Arguments from the caller

A rule can take an argument from whoever calls it ([#200](https://github.com/panyam/jaala/issues/200)). A head variable no relation in the body binds, which a comparison or a `not` reads instead, is an input: the rule has no answer of its own for it, and each call supplies one. Here the caller says how many lines a package needs:

{{ demo "demos/guide/modules-input-argument.yaml" }}

jaala answers such a rule per call, by writing its body into the caller with the argument filled in, so `at_least(?p, 500)` runs as `loc(?p, ?n), ?n >= 500`. A call can pass a value, a variable the rest of its own clause binds (in any order, since a comparison waits for what it compares), or a variable the host binds. One that leaves the argument unbound is refused before anything runs:

{{ demo "demos/guide/modules-input-argument-unbound.yaml" }}

That's how a library offers a question with a blank in it, such as "which nodes span line N of this file", without a predicate in Go. `Lookup` marks the argument with `Input` in the member's signature. A few shapes aren't supported yet. The rule has to be its relation's only one and can't call itself, which leaves its unbound variable the error it always was. A call can't be negated. And a caller counting its bindings (`count`, `sum` or `list` without `distinct`) can't call one whose body binds variables its head drops, since writing the body in would count those too.

## Mistakes in a module

`Check` resolves every module once, after the host has added them, and refuses a module with a mistake in it before any query runs. The error reads as it would in a query, and it's a `*ns.ModuleError`, which carries the module's path and origin. So a host that loads a whole directory of `.dl` files (`AddModulesFS`) can say which file to fix.
