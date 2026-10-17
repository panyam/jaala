---
title: "Asking through a model"
description: "What a host hands a model so it can write a program from a question: the vocabulary as a card, a grammar that keeps its output parseable and to the vocabulary, errors that say where the program is wrong and how to fix it, the loop that retries until a program checks, and scoring it over questions with known answers."
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

## The grammar

`nl.Grammar()` is the program syntax as a GBNF grammar, the form llama.cpp's `grammar` field takes ([#323](https://github.com/panyam/jaala/issues/323)). A model decoding under it can only write text that parses, so no retry is spent on a missing bracket or a misplaced `=>`. The fourth example above prints its first rules; the whole grammar is [`frontends/nl/testdata/program.gbnf`](https://github.com/panyam/jaala/blob/main/frontends/nl/testdata/program.gbnf).

It covers jaala's own spelling, the one these docs use: clauses separated by `; `, `not` for negation, the answer after ` => ` with `having`, `order by`, `after`, `limit` and `offset`. It leaves out the forms jaala reads only so people and agents can write other Datalogs (comments, a `.` ending, `?-`, `!atom`, see [Syntax]({{.Site.PathPrefix}}/guide/syntax/)), and a type that names another head variable (`?k`, `kind(?o)`), which only the parser can check. It allows one space or newline where the spelling has room for one, so a model can't spend tokens on blank space, and caps a name (a relation's segment, a variable, a unit) at 32 characters, so a model that wants to write prose can't run one name on until its token limit ([#340](https://github.com/panyam/jaala/issues/340)).

`nl.GrammarFor(v)` narrows it to a vocabulary ([#324](https://github.com/panyam/jaala/issues/324)). A call names one of `v`'s members at an arity it takes, and an argument whose type is a closed domain is a variable or one of the domain's values, so a model decoding under it can't write `edge(?a)`, a member that doesn't exist, or a misspelled kind. A program's own rules take names nothing at the root of `v` uses, so a call to any other name is a call to one of them, which the grammar can't check is defined. `nl.GrammarFor(v, nl.GoalOnly())` leaves rules out, so every call is a real member; that's the strictest form, for a small model asking one question. Both are built once per state of the vocabulary, as the card is.

Parsing, and with `GrammarFor` the names and counts, is all a grammar promises. Whether every variable is bound, and whether a rule a program calls exists, is still `Validate`'s, and its errors are the next section's.

## Errors a model can fix from

A program that can't run as written fails `Validate` and `Eval` with a `*datalog.CheckError`. Its message is the one the [error catalogue]({{.Site.PathPrefix}}/reference/errors/) lists, unchanged, and its fields say the same as data:

- `Kind` sorts it: `unknown`, `arity`, `unbound`, `type`, `stratify`, `definition`, `aggregate` or `answer`. A host counts failed programs by kind without matching on text.
- `Fix` says how to change the program, when the engine can tell: the vocabulary's suggestion for a name, or the program's own rule it likely means (`str.mine` for a rule `mine`, [#341](https://github.com/panyam/jaala/issues/341)), the signature for a call at the wrong arity, what to bind for an unbound variable. It's empty for a cycle through negation, where the fix depends on what the program meant.
- `Relation`, `Args`, `Var` and `Rule` say what the error is about.

A `Query` carries no positions: a host can build one in Go, and two parses of one text compare equal. So `datalog.Locate(text, err)` finds the place in the text instead, the call to `Relation` with `Args` arguments (in `Rule`'s clauses when it names one), else the first `?Var`. It returns a `ParseError`'s own position too, so one call places any error. `nl.Explain(text, err)` writes all of it the way a model reads it, the second example above: the message, the line with a caret under the place, and the fix.

## The loop

`nl.Translate` puts the card and the errors together ([#325](https://github.com/panyam/jaala/issues/325)). It asks the host's model for a program, parses and checks it, and when it doesn't check, sends the draft back with `Explain`'s text and asks again, up to `Options.MaxTries` times (3 by default). The third example above is a scripted model getting a name wrong once: the second prompt carries the first draft and its error, and the second draft checks. A reply wrapped in a Markdown code fence is read from inside the fence.

Every prompt carries `GrammarFor(v)` as `Prompt.Grammar`, or the host's own with `Options.Grammar` (none with `Options.NoGrammar`), for a model whose decoder takes one. A model that can't use it ignores it, since every draft is checked anyway; the grammar only saves tries.

jaala runs no model, but it ships clients for local ones already running, in a module of its own so no host takes them on unasked ([#330](https://github.com/panyam/jaala/issues/330)): `github.com/panyam/jaala/frontends/nlmodels/llamacpp` asks llama.cpp's `llama-server`, sending the prompt's grammar and keeping its prefix cached, and `frontends/nlmodels/ollama` asks an Ollama server, which takes no GBNF grammar, so its model writes freely. Otherwise a host implements `nl.Model`, a name and a `Complete` taking an `nl.Prompt`: a `Prefix` holding the card and a few lines of instructions, the same for every question over one vocabulary so a client can send it as a cached prefix, and the conversation so far as `Turns`. A host that can find the names in a question, as Declaire's `name.find` turns a short name into ranked ids, implements `nl.Grounder`, and the candidates go after the question with their values quoted, so the model copies a value rather than spelling it.

`Options.Fallback` is a second model, usually a bigger one, asked once the first runs out of tries. It starts a fresh conversation with the question, since the first model's drafts would only cost it tokens. A model that sends back a draft it was already told is wrong has stopped reading the errors, so that try is refused unchecked and the question moves to the fallback, or `Translate` stops when there is none ([#340](https://github.com/panyam/jaala/issues/340)). With `Options.Base`, a program that checks is run too (with `Options.Eval`'s options, such as a `Budget`): an error is fed back like a check error, and a program answering nothing goes straight to the fallback, told that one did, or back to the same model when no fallback is left. A `Complete` that fails moves to the fallback as well.

`Translate` returns the accepted `nl.Program` (its text, `Query`, rows when it ran, and the model that wrote it) and an `nl.Trace` of every try, also when it fails: the model, the text, the error and its `Kind`, tokens in, out and cached, and latency. `Kind` is a `CheckError`'s kind, or `parse`, `eval`, `empty`, `repeat` or `model`, so a host counts where its retries went without reading messages.

## Scoring a translator

`nl.Eval` runs a set of `nl.Case`s, each a question and the rows a right program answers, through a translator and a base, and reports how it went ([#326](https://github.com/panyam/jaala/issues/326)). The translator is a function from a question to `Translate`'s results, usually `Translate` closed over a vocabulary, a model and options, so a host can wrap its own fallback or tool around it. A program whose rows `Translate` already ran over `Options.Base` is scored on those; any other runs over the base given to `Eval`. A case failing, in translating or evaluating, fails alone.

The model chooses the program's columns, so a row matches by its values whatever their names and order: `=> ?b, ?a` answers `{"app", "api"}` as well as `=> ?a, ?b` does. Rows match as a multiset, in order only when the case sets `Ordered`, and an absent value is written `absent`. The last example above scores three questions, two needing a second draft.

The `nl.Report` holds each case's `Result` (the program, its trace, the rows it got and whether they passed) and the totals: passes on the first try and after retries, tries, tokens in, out and cached, latency, the refused tries by kind, and how many of the refused check errors carried a fix. That last pair says how often a model was told how to fix its mistake, and a retry spent on an error without one is where an error message could say more. `Report.String` writes it as the example does. jaala's own `go test ./frontends/nl -run TestEval -v` scores a scripted model and fallback the same way, over eight questions on this vocabulary.
