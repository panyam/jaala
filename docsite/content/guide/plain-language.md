---
title: "Asking through a model"
description: "What a host hands a model so it can write a program from a question: the vocabulary as a card, a grammar that keeps its output parseable and to the vocabulary, errors that say where the program is wrong and how to fix it, the loop that retries until a program checks, local models, a domain pack, and scoring it over questions with known answers."
prev: {url: "/jaala/guide/versions/", title: "Versions"}
next: {url: "/jaala/guide/mcp-server/", title: "Serving agents"}
---

A host that lets someone ask in plain language puts a model in front of jaala: the model writes a program from the question, jaala checks it, and a program that doesn't check goes back to the model with its error. Package `nl` holds the parts of that every host would otherwise write alike ([#321](https://github.com/panyam/jaala/issues/321)). What the words mean, the model and the tool that serves it stay with the host. An engine-only host never imports `nl`.

Each front end is a Go module of its own, so what one depends on reaches only the hosts that import it: `go get github.com/panyam/jaala/frontends/nl` for this page, `frontends/nlmodels` for the local model clients below, and `frontends/mcp` and `frontends/cypher` for the [next]({{.Site.PathPrefix}}/guide/mcp-server/) [two]({{.Site.PathPrefix}}/guide/cypher/) pages. They're tagged with jaala, `frontends/nl/v0.1.N` beside `v0.1.N`. Up to v0.1.71 `nl` was a package inside jaala's own module, so a host moving past that tag requires the new module in its place.

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

A model is the host's: `Translate` calls an `nl.Model`, a name and a `Complete` taking an `nl.Prompt`, which is a `Prefix` holding the card and a few lines of instructions, the same for every question over one vocabulary so a client can send it as a cached prefix, and the conversation so far as `Turns`. The local model clients below are two such models. A host that can find the names in a question, as Declaire's `name.find` turns a short name into ranked ids, implements `nl.Grounder`, and the candidates go after the question with their values quoted, so the model copies a value rather than spelling it.

`Options.Fallback` is a second model, usually a bigger one, asked once the first runs out of tries. It starts a fresh conversation with the question, since the first model's drafts would only cost it tokens. A model that sends back a draft it was already told is wrong has stopped reading the errors, so that try is refused unchecked and the question moves to the fallback, or `Translate` stops when there is none ([#340](https://github.com/panyam/jaala/issues/340)). With `Options.Base`, a program that checks is run too (with `Options.Eval`'s options, such as a `Budget`): an error is fed back like a check error, and a program answering nothing goes straight to the fallback, told that one did, or back to the same model when no fallback is left. That model is told which literal matched nothing and what it was first called with (`Report.Empty`, see [Host options](../host-options/), [#343](https://github.com/panyam/jaala/issues/343)), so it can change that call rather than guess, and whether the call matches anything by itself ([#348](https://github.com/panyam/jaala/issues/348)), which tells a wrong value from a wrong join. A `Complete` that fails moves to the fallback as well.

`Translate` returns the accepted `nl.Program` (its text, `Query`, rows when it ran, and the model that wrote it) and an `nl.Trace` of every try, also when it fails: the model, the text, the error and its `Kind`, tokens in, out and cached, latency, and, for a draft run over `Options.Base`, the `datalog.Report` of that run. An empty answer's error is an `nl.EmptyAnswer`, whose `Why` is the run's `Report.Empty`. `Kind` is a `CheckError`'s kind, or `parse`, `eval`, `empty`, `repeat`, `answer` or `model`, so a host counts where its retries went without reading messages.

## Looking before answering

A model drafting blind guesses at what the data holds, and on Declaire's questions most of a local model's failures were guesses that answered nothing: an edge kind, an attribute's value, an id's spelling ([#393](https://github.com/panyam/jaala/issues/393)). `Options.Actions` lets it look first. Each reply may then be an action instead of an answer, and its result comes back as the next turn:

- `look NAME`: a relation's signature and whole doc, or a module's members.
- `values TARGET`: up to `Actions.Sample` (10) of one argument's values, as `pkg.license` or `pkg.1`, run as `pkg(_, ?v) => ?v limit 10`.
- `try` on its own line, then a draft: it runs with at most `Sample` rows, and sends back its rows, or why it answered none, as an empty answer is explained.

Any other reply is the answer, judged as a draft always is. An action doesn't spend a try. `Actions.Steps` (6) caps them per model, a repeated one is answered with a note that it was asked already, and once they run out the next reply must be the answer, under the answer's grammar. The grammar sent wraps the draft's, so a model under GBNF writes only a well-formed action or a draft. `Trace.Steps` records each action with what went back and what it cost, and `Report` counts steps and their tokens beside the tries, so the first-try rate still counts answers.

The loop doesn't read the drafts' language: a `try` goes through the front end as an answer does, and a front end that implements `nl.Prober` answers `look` and `values` in its own terms. Cypher's does, so a model writing Cypher asks `values Package.license` and the probe runs as `MATCH (n:Package) RETURN DISTINCT n.license LIMIT 10`.

## Front ends

A model needn't write jaala's syntax. `Options.FrontEnd` names a language it writes instead ([#354](https://github.com/panyam/jaala/issues/354)), and Cypher's is `cypher.NewFrontEnd(graph, examples...)` ([asking in Cypher]({{.Site.PathPrefix}}/guide/cypher/#a-model-writing-cypher)). The prompt is then the front end's card, instructions and examples, followed by the pack's `About` and data `Card` but not its jaala examples. The grammar sent is the front end's. Each draft is compiled before it's checked and run, and every refusal goes back as the front end explains it, in the draft's language and at its place in the draft. Everything else is unchanged: repeats, empty answers, the fallback and the pack's `Check`. An empty answer goes back at the draft's place too: `cypher.Explain` puts the caret under the pattern that wrote the literal `Report.Empty` ends at ([#386](https://github.com/panyam/jaala/issues/386)).

Each `Try` and the accepted `Program` record the `Language`, and `Program.Compiled` holds the jaala program the draft became. A compile refusal is the kind `compile`, or the front end's own, such as Cypher's `gap` for a construct jaala can't express yet. `Report.String` names the language, so a host can run one set of questions through both languages on one model and compare the two reports: first-try passes, tries, tokens and what was refused.

A front end is an interface in `nl` (`nl.FrontEnd`) that the front end's own module implements, since `nl` imports no front end.

## Local models

jaala runs no model, but `github.com/panyam/jaala/frontends/nlmodels`, a module of its own, has clients for local ones already running, so no host takes on HTTP clients it doesn't use ([#330](https://github.com/panyam/jaala/issues/330)):

- `llamacpp.New(url)` asks llama.cpp's `llama-server` at its OpenAI-style `/v1/chat/completions`. It sends the prefix as the system message, the prompt's grammar as `grammar`, so the model can only write text that parses, and `cache_prompt`, so the card is processed once. Its `Name` is `llama.cpp`, or `llama.cpp:` and `Model` when one is set.
- `ollama.New(url, model)` asks an Ollama server for one model, named `ollama:` and the model. Ollama takes no GBNF grammar (its `format` is a JSON schema), so the model writes freely and costs more tries; a host wanting the grammar held runs the same weights under llama.cpp.

Both reply with at most `MaxTokens` (512 by default) at `Temperature` 0, and take an `HTTP` client of the host's. The tokens a try reports are the server's own counts, with llama.cpp's cached prompt tokens. Here a stand-in for `llama-server` answers one program and prints what it was sent:

```go
{{ includeFileText "examples/guide/localmodel_test.go" }}
```

## Domain packs

The card says what a program can call, but not what the data holds or what a question in this domain usually looks like. A host says that once, in an `nl.Pack` passed as `Options.Pack` ([#344](https://github.com/panyam/jaala/issues/344)), so Declaire's code graph and Agni's schematics are each a pack over the same `Translate`:

- `About` says what the data is about, in the words its users use. It opens the pack's part of the prefix, and jaala's MCP server ([#347](https://github.com/panyam/jaala/issues/347)) writes its instructions from it.
- `Card` says what the data holds that the vocabulary can't, such as which kinds and values a question will name. `nl.DataCard(ctx, base)` writes the generic half: each relation with how many facts it holds and, per argument, its values with how many facts hold each, or how many there are and the commonest when there are more than twelve. It counts every relation whole, so a host computes it once per data version and adds what only it knows, such as how its ids are spelled.
- `Examples` are questions with the programs that answer them, one or two per construct. How to call one relation belongs in its doc instead.
- `Grounder` grounds the question when `Translate` is given none.
- `Check` judges an answer that checked and ran: an error refuses the draft as `answer` and goes back to the same model with its text, as a check error does. It's for what makes an answer suspect in the domain, such as every value absent, or a hundred rows for a question asking for one.

The pack's part goes after the card and the instructions, `About` and the examples as comments, so the prefix is still the same for every question and stays cached. `Translate` checks every example against the vocabulary before asking a model, and fails naming the first that doesn't check, so a pack can't teach a model a relation the vocabulary lacks. A host's test can call `Pack.Validate` and `Pack.Prefix` itself. Here `DataCard` writes the guide's data, and a pack whose `Check` expects one package refuses a first draft answering three:

```go
{{ includeFileText "examples/guide/pack_test.go" }}
```

## Serving agents

The same pack makes an MCP server, so an agent asks over the vocabulary with its own tools: [Serving agents]({{.Site.PathPrefix}}/guide/mcp-server/) covers `frontends/mcp`.

## Scoring a translator

`nl.Eval` runs a set of `nl.Case`s, each a question and the rows a right program answers, through a translator and a base, and reports how it went ([#326](https://github.com/panyam/jaala/issues/326)). The translator is a function from a question to `Translate`'s results, usually `Translate` closed over a vocabulary, a model and options, so a host can wrap its own fallback or tool around it. A program whose rows `Translate` already ran over `Options.Base` is scored on those; any other runs over the base given to `Eval`. A case failing, in translating or evaluating, fails alone.

The model chooses the program's columns, so a row matches by its values whatever their names and order: `=> ?b, ?a` answers `{"app", "api"}` as well as `=> ?a, ?b` does. Rows match as a multiset, in order only when the case sets `Ordered`, and an absent value is written `absent`. The last example above scores three questions, two needing a second draft.

The `nl.Report` holds each case's `Result` (the program, its trace, the rows it got and whether they passed) and the totals: passes on the first try and after retries, tries, tokens in, out and cached, latency, the refused tries by kind, and how many of the refused check errors carried a fix. That last pair says how often a model was told how to fix its mistake, and a retry spent on an error without one is where an error message could say more. `Report.String` writes it as the example does. When `Translate` ran the programs over a `Base`, it adds each case's work and the three costliest drafts with the literal that spent the most (`Report.Costliest`), ranked by work rather than time, so two runs rank them the same. jaala's own `TestEval` (`cd frontends/nl && go test -run TestEval -v .`) scores a scripted model and fallback the same way, over eight questions on this vocabulary.
