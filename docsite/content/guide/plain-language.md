---
title: "Asking through a model"
description: "What a host hands a model so it can write a program from a question: the vocabulary as a card, and errors that say where the program is wrong and how to fix it."
prev: {url: "/jaala/guide/versions/", title: "Versions"}
---

A host that lets someone ask in plain language puts a model in front of jaala: the model writes a program from the question, jaala checks it, and a program that doesn't check goes back to the model with its error. Package `nl` holds the parts of that every host would otherwise write alike ([#321](https://github.com/panyam/jaala/issues/321)). What the words mean, the model and the tool that serves it stay with the host. An engine-only host never imports `nl`.

The examples use the vocabulary from [Host options]({{.Site.PathPrefix}}/guide/host-options/): the `imports` and `pkg` relations, the standard predicates and the `deps` module.

## The card

`nl.Card` lists every relation and predicate a program can call, one line each: the signature, what a predicate needs bound before it runs, and the first line of the doc. A model reads it in place of the docs. The same vocabulary always gives the same text, so a host can keep it as a cached prompt prefix and pay for it once.

```go
{{ includeFileText "examples/guide/plainlang_test.go" }}
```

A type written `~node` is one the engine inferred from a derived relation's rules rather than one its head declares, and `in` marks an argument the caller has to bind ([#200](https://github.com/panyam/jaala/issues/200)). `filter` means every argument has to be bound; a generator lists the arguments each of its modes needs, `needs a or b`.

## Errors a model can fix from

A program that can't run as written fails `Validate` and `Eval` with a `*datalog.CheckError`. Its message is the one the [error catalogue]({{.Site.PathPrefix}}/reference/errors/) lists, unchanged, and its fields say the same as data:

- `Kind` sorts it: `unknown`, `arity`, `unbound`, `type`, `stratify`, `definition`, `aggregate` or `answer`. A host counts failed programs by kind without matching on text.
- `Fix` says how to change the program, when the engine can tell: the vocabulary's suggestion for a name, the signature for a call at the wrong arity, what to bind for an unbound variable. It's empty for a cycle through negation, where the fix depends on what the program meant.
- `Relation`, `Args`, `Var` and `Rule` say what the error is about.

A `Query` carries no positions: a host can build one in Go, and two parses of one text compare equal. So `datalog.Locate(text, err)` finds the place in the text instead, the call to `Relation` with `Args` arguments (in `Rule`'s clauses when it names one), else the first `?Var`. It returns a `ParseError`'s own position too, so one call places any error. `nl.Explain(text, err)` writes all of it the way a model reads it, the second example above: the message, the line with a caret under the place, and the fix.
