---
title: "Error messages"
description: "Every error jaala can return, what causes it, and an example of each one a query can trigger."
---

Every error jaala returns starts with `query:`, so a host can tell a mistake in a question from a failure elsewhere. The wording of each message stays stable once it ships, since hosts match on fragments of it, and a new case gets new wording rather than moving an old one. This page lists every message in jaala's source, which a test checks when the site is built, so a message added to the engine fails the docs build until it's listed here.

In each message, `…` stands for the part that names your relation, variable or value. Where a query can trigger it, an example follows, pinned to the message's wording, and you can edit it in the page.

## Reading the query

The parser reads the text before anything else runs, so these come first and say where the text went wrong. Each is a `*datalog.ParseError`, whose `Line` and `Column` give where the piece it's about starts.

`query: no goal clause (a query needs one clause without … to ask)`

Every query ends in one goal, a clause without `:-`. Text with only rules asks nothing.

{{ demo "demos/errors/no-goal-clause.yaml" }}

`query: … goal clauses; a query asks one goal (rules use …, the goal does not)`

A query has exactly one goal. A missing `:-` turns a rule into a second goal.

{{ demo "demos/errors/goal-clauses-a-query-asks-one-goal.yaml" }}

`query: … is a goal; rules-only text defines relations and asks nothing`

Raised by `ParseRules`, which reads rules for a module and refuses a goal among them.

`query: more than one … projection separator`

A goal has one `=>`.

{{ demo "demos/errors/more-than-one-q-projection-separator.yaml" }}

`query: clause … has more than one …`

A rule has one `:-`, usually missing a `;` between two rules.

{{ demo "demos/errors/has-more-than-one-q.yaml" }}

`query: … has no goal before it; the goal goes first, as in …`

A `=>` with columns after it and no goal before, as in `r(?p) :- package(?p). => ?p, count(?p)`. Write the goal before the `=>`: `r(?p) => ?p, count(?p)`. A goal alone after `=>`, as `=> r(?p)`, is read as the goal `r(?p)`.

{{ demo "demos/errors/has-no-goal-before-it.yaml" }}

`query: the rule for … holds …; end the rule with … or …, then write the goal`

A rule's body runs into the goal's `=>`, because nothing ended the rule.

{{ demo "demos/errors/the-rule-holds-arrow.yaml" }}

`query: empty query`

A query with no text at all, from a host that built one empty.

`query: malformed atom … (want reln(args))`

An atom is a relation name and its arguments in parentheses. An unclosed string or parenthesis usually causes this.

{{ demo "demos/errors/malformed-atom.yaml" }}

`query: "as" names the fact an atom matched with one ?variable, as edge(?a, ?b) as ?r`

What follows `as` after an atom has to be one variable, which takes the fact the atom matched ([#279](https://github.com/panyam/jaala/issues/279)).

{{ demo "demos/errors/fact-as-names-one-variable.yaml" }}

`query: bad relation name …`

A relation name is identifiers joined by `.` or `-`, with no spaces.

{{ demo "demos/errors/bad-relation-name.yaml" }}

`query: … is neither an atom nor a comparison`

A literal is an atom, a negated atom or a comparison.

{{ demo "demos/errors/is-neither-an-atom-nor-a-comparison.yaml" }}

`query: empty term`

An argument is empty, as in `imports(?a,,?b)` or `imports(?a,)`.

{{ demo "demos/errors/empty-term.yaml" }}

`query: empty literal`

A literal is empty, as in `imports(?a, ?b), , => ?a`. A query built in Go can hold one too.

{{ demo "demos/errors/empty-literal.yaml" }}

Before v0.1.24 the parser dropped empty arguments and literals and ran what was left, so a doubled comma could change a query's arity without a word. Remove the extra comma. A call with no arguments, like `flag()`, is still fine.

`query: empty variable name`

A `?` with no name after it.

`query: variable ?…: a variable's name is letters, digits and _`

A variable whose name has anything else in it, such as `?a.b` or the `?)(` a stray parenthesis makes. Relations can have dots and hyphens in their names; variables can't.

{{ demo "demos/errors/empty-variable-name.yaml" }}

`query: unterminated string …`

A string is missing its closing `"`. A `"` after a backslash doesn't close one, so a string ending in a backslash is written `"a\\"` (both backslashes stay in it).

{{ demo "demos/errors/unterminated-string.yaml" }}

`query: … is more than one string; put a backslash before a quote inside a string`

Two strings where a term goes, as `"api" "cli"` without a comma between them. Versions before v0.1.61 read that as the one string `api" "cli`. A quote inside a string follows a backslash: `"say \"hi\""`.

{{ demo "demos/errors/more-than-one-string.yaml" }}

`query: bare identifier … — a term must be a ?variable, a "string", or a number`

A term is a `?variable`, a quoted string or a number. A word without quotes is none of them.

`query: bare identifier … — a term must be a ?variable, a "string", or a number; write ?… for a variable or … for text`

The same, for a word that could be either: `api` is `?api` as a variable or `"api"` as text. jaala doesn't guess, since a constant read as a variable would match everything.

{{ demo "demos/errors/bare-identifier.yaml" }}

`query: projection column … must be a ?variable or an aggregate`

The columns after `=>` are variables, aggregates or arithmetic, such as `?i * ?r`.

{{ demo "demos/errors/projection-column.yaml" }}

`query: the projection has an empty column, as in …`

A column after `=>` is empty. Remove the extra comma.

{{ demo "demos/errors/empty-projection-column.yaml" }}

`query: malformed aggregate …`

An aggregate is a function name and one variable in parentheses.

{{ demo "demos/errors/malformed-aggregate.yaml" }}

`query: … must name the variable an aggregate binds, as ?v = count(?x) : { ... }`

An aggregate in a body, written with braces, binds a variable: `?n = count(?t) : { ... }`.

`query: … is not an aggregate, as count(?x)`

The right side of `=` before the braces is an aggregate function over one variable.

`query: an aggregate binds a ?variable, not …`

The left side is a variable, not `_` or a constant.

`query: an aggregate's body … must close with }`

The braces hold the aggregate's body and close it.

`query: the aggregate binding ?… has an empty body`

There's nothing in the braces to reduce.

{{ demo "demos/errors/aggregate-binding-has-an-empty-body.yaml" }}

`query: aggregate …(...) expects a ?variable, got …`

An aggregate reduces a variable, not a constant.

{{ demo "demos/errors/expects-a-variable-got.yaml" }}

`query: … appears twice after …`

Each clause after the projection can appear once.

{{ demo "demos/errors/appears-twice-after.yaml" }}

`query: … comes after …, not before it (the order is having, order by, after, limit, offset)`

The clauses after the projection come in a fixed order.

{{ demo "demos/errors/comes-after-s-not-before-it.yaml" }}

`query: order needs by, as in …`

It is `order by`.

{{ demo "demos/errors/order-needs-by.yaml" }}

`query: order by needs a column, as in …`

`order by` names at least one column.

{{ demo "demos/errors/order-by-needs-a-column.yaml" }}

`query: order by: …`

Wraps an error in one of `order by`'s columns, omitting the underlying error's leading `query:` prefix.

{{ demo "demos/errors/order-by-wrapped.yaml" }}

`query: … in … compares number[…] with number[…], which never holds`

An ordering comparison between two sides whose units are both known and differ, such as volts against amps, can never hold, so it's refused before anything runs ([#190](https://github.com/panyam/jaala/issues/190)), in the goal, a rule, a library module (at `Check`) or a `having`. A unit is known for a constant the host bound with one, or for a variable every relation binding it declares in that unit. A source declares units in its relations' schemas, so this comes from a host's data rather than the docs' examples, whose facts declare none. Where the data alone mixes units, the comparison is false at run time instead.

`query: …'s ?… is an input argument, and the call in … doesn't bind it`

A rule that takes an argument from its caller needs every call to bind it, with a value or a variable the call's own clause binds ([#200](https://github.com/panyam/jaala/issues/200)).

{{ demo "demos/errors/is-an-input-argument.yaml" }}

`query: … takes an argument from its caller, so … can't negate it`

Such a rule is answered by writing its body into each call, and a `not` over a body of several literals has no form to write.

`query: … counts its bindings, and …, which takes an argument from its caller, binds variables its head drops, so the count would include them; count(distinct ...) counts its tuples`

Writing a rule's body into a caller that counts bindings would count each value of the variables the rule's head leaves out. `count(distinct …)` counts what the rule holds.

`query: expression …: unexpected …`, `query: expression … ends early`, `query: expression …: unclosed (`, `query: expression …: want , or ) after …'s argument`

An expression in a comparison that doesn't parse: an operator with nothing after it, a parenthesis left open, or a function call missing a comma ([#5](https://github.com/panyam/jaala/issues/5)).

`query: arithmetic needs …, which stdlib.Arithmetic registers`

Arithmetic is written in the standard library's `num.*` functions, so a vocabulary without them can't run an expression. For a host that composes its own vocabulary.

`query: arithmetic needs two of its three values bound (a variable must appear in a relation before it is computed with)`, `query: arithmetic needs a value bound to negate (a variable must appear in a relation before it is computed with)`

An arithmetic function called with too little bound, which the planner refuses before running in a query; a host calling the function from Go can still meet it.

`query: rule … computes with arithmetic and reads …, which reaches it again, so it might never finish`

A rule that computes a value and feeds it back into its own recursion has no fixpoint ([#207](https://github.com/panyam/jaala/issues/207) would allow it under a bound).

{{ demo "demos/errors/computes-with-arithmetic.yaml" }}

`query: unit … isn't a name: letters only`, `query: unit … is defined twice`, `query: prefix … is defined twice`, `query: unit …: …`

`Vocabulary.AddUnit` refusing a name or a definition, for a host registering units.

`query: unit … isn't a base unit`, `query: unit … isn't defined with … to the first power`, `query: unit … is already written through …`

`Vocabulary.WriteBaseThrough` refusing a pair: the first unit has to be a base unit, the second a unit defined with it to the first power (the standard library writes kg through V), and each base unit is written through one unit at most.

`query: after takes values, as in …, and … is not one`

`after` lists the values a page continues from, one per `order by` column, not variables.

{{ demo "demos/errors/after-takes-values.yaml" }}

`query: … … is not …`

`limit` takes a positive whole number, and `offset` a whole number.

{{ demo "demos/errors/is-not-s.yaml" }}

`query: having …: …`

Wraps an error inside a `having` condition, omitting the underlying error's leading `query:` prefix.

{{ demo "demos/errors/having-q-w.yaml" }}

`query: having needs a comparison, as in …`

A comparison in `having` is empty. Remove the extra comma.

{{ demo "demos/errors/empty-having-comparison.yaml" }}

`query: having … filters ?…, which is a group key rather than an aggregate — a comparison over plain variables belongs in the goal, before the …`

`having` filters groups by an aggregate. A condition on a plain variable belongs in the goal.

{{ demo "demos/errors/which-is-a-group-key-rather-than-an-aggr.yaml" }}

`query: having … is not a comparison (want an aggregate, an operator and a value, as in …)`

A `having` condition compares an aggregate with something.

{{ demo "demos/errors/is-not-a-comparison-want-an-aggregate.yaml" }}

`query: only a ?variable can declare a type, not …`

Only a variable in a rule head can declare a type.

{{ demo "demos/errors/only-a-variable-can-declare-a-type.yaml" }}

`query: …'s type … must name another variable of its head`

A declared type that names another argument, like an owner, has to name one of the same head.

{{ demo "demos/errors/must-name-another-variable-of-its-head.yaml" }}

`query: empty type declaration`

A `:` with no type after it.

{{ demo "demos/errors/empty-type-declaration.yaml" }}

`query: bad kind variable …`

A kind written with a malformed variable.

`query: bad type declaration … (an owner is written kind(?var))`

An entity kind's owner is written `kind(?var)`.

{{ demo "demos/errors/an-owner-is-written-kind-var.yaml" }}

`query: bad type declaration …`

A type that is none of the forms the grammar allows.

`query: unterminated vocabulary in …`

A closed set of values, `{"a", "b"}`, missing its `}`.

`query: vocabulary entry … must be a "string"`

Each value in a closed set is a quoted string.

{{ demo "demos/errors/vocabulary-entry-q-must-be-a.yaml" }}

## Names and arity

jaala looks every name up in the vocabulary, and suggests a close one when a name is missing.

`query: unknown relation …`

A name the vocabulary doesn't hold. When a close name exists, the message suggests it, and so does a member of the module whose name is one word of the name written (`str.has_suffix` suggests `str.suffix`). With no suggestion, an unknown member of a module that exists lists what the module holds.

{{ demo "demos/errors/unknown-relation-q-s.yaml" }}

`query: … is a module, not a relation; it holds …`

A module's path used as a relation. The message lists what the module holds.

{{ demo "demos/errors/is-a-module-not-a-relation-it-holds.yaml" }}

`query: unknown module … in …`

The first part of a path names no module, which is a different typo from a wrong last part.

{{ demo "demos/errors/unknown-module-q-in-q.yaml" }}

`query: not … as ?… names no fact: a negated atom matched none`

A negated atom holds when nothing matches it, so there's no fact for `as` to name.

{{ demo "demos/errors/fact-negated.yaml" }}

`query: … is a predicate, whose matches are no stored facts, so "as ?…" names none`

A predicate's matches are computed, not stored, so they have no ids.

{{ demo "demos/errors/fact-predicate.yaml" }}

`query: … is derived, and only a base relation's facts have ids, so "as ?…" names none`

A derived relation's tuples come from rules, not a store. Name the facts of the base relations its rules read instead.

{{ demo "demos/errors/fact-derived.yaml" }}

`query: relation … takes … args, got …`

A relation called with the wrong number of arguments.

{{ demo "demos/errors/relation-q-takes-d-args-got-d.yaml" }}

`query: … takes … args, got …`

A predicate such as `str.contains` called with the wrong number of arguments.

{{ demo "demos/errors/takes-s-args-got-d.yaml" }}

`query: negation over …`

A `not` over a name the vocabulary doesn't hold.

{{ demo "demos/errors/negation-over.yaml" }}

`query: negated relation … takes … args, got …`

A `not` with the wrong number of arguments. Over a rewritten relation it can name the rewrite ([#127](https://github.com/panyam/jaala/issues/127)).

{{ demo "demos/errors/negated-relation-q-takes.yaml" }}

`query: graph.closure takes 6 args, got …`, `query: graph.closure's relation must be a text constant naming one, not …`

`graph.closure("relation", ?from, ?to, min, max, ?hops)` names the relation it walks as text in its first argument, and takes all six ([#281](https://github.com/panyam/jaala/issues/281)). Write `_` for the hops you don't read and `0` for no max.

`query: graph.closure needs a relation of 2 args, from and to; … takes …`, `query: graph.closure can't walk …, a predicate: it walks a relation's facts`, `query: graph.closure can't walk …, which takes an argument from its caller`

A closure walks a relation's facts from their first argument to their second. To walk a wider relation, such as `edge(from, kind, to)`, write a rule of two arguments reading it, `calls(?a, ?b) :- edge(?a, "CALLS", ?b)`, and walk that. A relation it can't name at all is reported as `query: graph.closure over unknown relation …`.

`query: optional { … } needs a relation outside its braces to match first: optional keeps a row the rest of its clause matched`, `query: optional { … } holds another optional in its braces: write them one after the other`

An optional keeps the rows its clause finds without it, so the clause needs a positive relation outside the braces ([#282](https://github.com/panyam/jaala/issues/282)). Two optionals written one after the other match in turn, as two `OPTIONAL MATCH`es do.

`query: optional { … } reads ?…, which no relation binds: one inside the braces or outside them must`, `query: optional { … } shares ?… with the rest of its clause, so a relation outside the braces that reads nothing they bind must bind it`

A variable the braces share with the rest of the clause has to be bound on one side: outside, by a relation that doesn't need what the braces bind, or inside, when it's what the optional binds and is absent without a match.

`query: an optional's body … must close with }`, `query: optional { } holds nothing to match`

`optional` is followed by braces holding at least one literal.

`query: graph.paths needs a relation of 2 args, from and to, or 3, from, edge and to; … takes …`

`graph.paths` walks a relation of two arguments, or of three when the middle one names the edge ([#280](https://github.com/panyam/jaala/issues/280)). Its other refusals read as `graph.closure`'s do, with its own name: `graph.paths takes 7 args`, a relation that isn't a text constant, an unknown relation, a predicate, or one taking an argument from its caller.

## Rules

Rules are checked as a whole before any of them runs.

`query: rule head … redefines a fact relation`

A query's rule can't define a relation the source already serves.

{{ demo "demos/errors/redefines-a-fact-relation.yaml" }}

`query: rule head … redefines a built-in relation`

A query's rule can't define a built-in.

{{ demo "demos/errors/redefines-a-built-in-relation.yaml" }}

`query: rule head … is a qualified path; a query defines only its own bare relations, and a shared one is registered with AddModule`

A query defines its own relations, with bare names. A shared one belongs in a module.

{{ demo "demos/errors/is-a-qualified-path-a-query-defines-only.yaml" }}

`query: rule head … is a module, not a relation`

A rule head names a module.

{{ demo "demos/errors/is-a-module-not-a-relation.yaml" }}

`query: rule head … redefines a derived relation registered in module …`

A query's rule can't redefine a module's member. Raised when the host has a module defining that name.

`query: rule … defined with … and … args (arity must be consistent)`

Every rule of a relation has the same number of arguments.

{{ demo "demos/errors/defined-with-d-and-d-args.yaml" }}

`query: rule … aggregates, so it must be the relation's only rule (… define it)`

A rule that aggregates is its relation's only rule.

{{ demo "demos/errors/so-it-must-be-the-relation-s-only-rule.yaml" }}

`query: rule … reads …`

A rule reads a name the vocabulary doesn't hold.

{{ demo "demos/errors/rule-q-reads-s.yaml" }}

`query: rule … head variable ?… is not bound by a positive body relation`

Every variable of a rule's head has to be bound by a positive literal of its body.

{{ demo "demos/errors/head-variable-s-is-not-bound.yaml" }}

`query: rule … has _ in its head, which gives that place no value; use a variable its body binds, or a constant`

A derived tuple needs a value in every place, so `_` can't stand in a rule's head. Write the variable the body binds there, or a constant.

{{ demo "demos/errors/has-wildcard-in-its-head.yaml" }}

`query: … uses … in a literal; an aggregate can only stand in a rule head or the answer`

An aggregate stands in a rule head or the projection, not in a body. Raised for a query built in Go; in text the parser refuses it first.

`query: rules are not stratifiable (recursion through negation: …)`

A cycle of rules passes through a `not`, so no order derives them.

{{ demo "demos/errors/recursion-through-negation.yaml" }}

`query: … uses ?… inside its own braces; the aggregate binds it`

An aggregate in a body binds its variable once its braces are reduced, so they can't read it. A filter on the value goes after the braces.

{{ demo "demos/errors/uses-inside-its-own-braces.yaml" }}

`query: … holds another aggregate in its braces, which is not supported`

Aggregates in a body don't nest yet. A rule can name the inner one's result, and the braces can read that rule.

{{ demo "demos/errors/holds-another-aggregate.yaml" }}

`query: … shares ?… with the rest of its body, so a relation outside the braces must bind it (one that doesn't need an aggregate's value)`

A variable written both in the braces and outside them picks which group the braces reduce, so a relation outside has to bind it. One that reads an aggregate's value can't, since that value isn't known until the braces are reduced.

{{ demo "demos/errors/shares-outside-the-braces.yaml" }}

`query: … shares ?… with the rest of its body, so a relation inside the braces must bind it too`

The braces group by what they share, so a relation inside them has to bind it, not only a comparison or a `not`.

{{ demo "demos/errors/shares-inside-the-braces.yaml" }}

`query: … aggregates ?…, which no relation in its braces binds`

The variable an aggregate in a body reduces is bound by a relation inside its braces.

`query: two aggregates bind ?…`

Each aggregate in a body binds a variable of its own.

`query: rules are not stratifiable (recursion through an aggregate: …)`

A cycle of rules passes through an aggregate.

{{ demo "demos/errors/recursion-through-an-aggregate.yaml" }}

`query: rule head … is qualified; a module defines its own members, written bare`

Inside a module, a rule defines one of the module's own members, written bare. Raised by `Check`.

`query: the module defines … with … and … args (arity must be consistent)`

A module's rules for one member disagree on its arity. Raised by `Check`.

`query: module … rule … reads …`

A module's rule reads a name that doesn't exist. Raised by `Check`, with the module's origin on the `ModuleError`.

## Types

A constant is read as its argument's type, so a wrong one is refused rather than matching nothing. Several of these are raised by `Check` over a host's modules.

`query: … cannot be … (it holds a number)`

A constant in a number argument has to read as a number.

{{ demo "demos/errors/it-holds-a-number.yaml" }}

`query: ?… cannot be compared with …: it is a number (…)`

A number argument compared with text that isn't a number.

{{ demo "demos/errors/cannot-be-compared-with.yaml" }}

`query: …'s … argument cannot be … (it holds one of: …)`

A constant outside an argument's closed set of values. Raised for a host relation that declares one.

`query: …'s type … names ?…, which is not in its head`

A module member's declared type names a variable its head doesn't have. Raised by `Check`.

`query: … declares its … argument as both … and …`

Two rules of a module member declare one argument differently. Raised by `Check`.

`query: … declares ?…: …, but its rules make it …`

A module member declares a type its rules can't produce. Raised by `Check`.

`query: graph.closure's min must be a whole number of steps, 0 or more, not …`, `query: graph.closure's max must be a whole number of steps, 0 for no bound or at least min, not …`

A closure's bounds count steps. Min 0 lets `?from` reach itself in none, when the relation holds it, and max 0 means no bound. `graph.paths` reads its bounds the same way and says so in its own name.

`query: graph.paths's mode must be one of walk, trail, acyclic, simple, shortest, not …`, `query: graph.paths in mode walk needs a max: around a cycle its paths never end`

A paths call's fourth argument says how a path may repeat, and a walk may repeat anything, so with no max a cycle would give paths without end. Write a max, or walk as a trail or acyclic.

`query: a paths call's mode must be one of walk, trail, acyclic, simple, shortest, not …`, `query: a paths call in mode walk needs a within: around a cycle its paths never end`, `query: a paths call's within must be a whole number of steps, 0 for no bound`, `query: a paths call needs its mode and within bound`

The same for a host's predicate built with `stdlib.Paths` or `stdlib.EdgePaths`, whose `?within` is the max.

`query: a reach's within must be a whole number of steps, at least 1`, `query: a reach's within needs a number bound (the most steps a path may take)`

The same for a host's reach built with `stdlib.Hops`, whose within is the most steps a path may take.

## Running the query

These depend on the order variables get their values, so jaala checks them against the body as it will run.

`query: comparison operand is unbound (a variable must appear in a relation before it is compared)`

A comparison needs both sides to have values, so a variable it compares has to appear in an atom of the same goal or rule body. Where the comparison is written doesn't matter: it's checked once that atom has bound the variable. In a rule, this is refused before the query runs.

{{ demo "demos/errors/comparison-operand-is-unbound.yaml" }}

`query: … needs all arguments bound (a variable must appear in a relation before … tests it)`

A test like `str.contains` needs every argument bound by the rest of its body, in the goal or a rule. It's checked before anything runs, so it doesn't matter where in the body the test is written: `str.contains(?x, "R"), rail(?x, _)` runs the test once `rail` has bound `?x`.

{{ demo "demos/errors/needs-all-arguments-bound.yaml" }}

`` query: negated relation … shares no variable with the rest of the query (?… appears only inside the `not`, so the negation has nothing to range over and matches either every row or none) ``

A `not` has to share a variable with the positive part of the goal.

{{ demo "demos/errors/shares-no-variable-with-the-rest-of-the.yaml" }}

`query: projected ?… is not bound by a positive relation (a variable used only under negation is existential and cannot be selected)`

A column of the answer has to be bound by a positive atom.

{{ demo "demos/errors/projected-s-is-not-bound.yaml" }}

`query: … aggregates ?…, which no relation binds`

An aggregate's variable has to be bound by the goal.

{{ demo "demos/errors/aggregates-s-which-no-relation-binds.yaml" }}

`query: … calls …, and nothing binds what … needs first: it needs …`

A generator whose required inputs nothing in the body binds, such as `str.distance` with a string no relation gives a value, or a host's own generator.

{{ demo "demos/errors/nothing-binds-what-needs-first.yaml" }}

`query: str.distance needs both strings bound (a variable must appear in a relation before str.distance measures it)`

The same mistake caught while running rather than before: an evaluator that runs a goal in the order it's written reached `str.distance` before its strings had values. The planned evaluator hosts use moves it after them, so it reports the error above instead.

`query: … needs … bound (a variable must appear in a relation before … reads it)`

The same mistake for a text function, such as `str.concat` or `str.split`: an evaluator running the goal as written reached it before what it reads had values. The planned evaluator moves it after them, and with nothing binding them refuses the goal before it runs, with the message above.

`query: cannot bind …: the goal does not use it`

`Bind` names a variable the goal doesn't use, usually a typo.

{{ demo "demos/errors/cannot-bind-s-the-goal-does-not-use-it.yaml" }}

`query: invalid regex …: …`

A pattern given to `str.match` doesn't compile. The message carries the compiler's reason.

{{ demo "demos/errors/invalid-regex.yaml" }}

`query: invalid glob …: …`

A glob given to `str.glob` doesn't compile.

{{ demo "demos/errors/invalid-glob.yaml" }}

`query: Materialize: … is not a derived relation`

`SemiNaive.Materialize` was asked to keep a relation that no rule of the query and no module member derives, such as a base relation or a typo. Only a derived relation has anything to keep.

`query: Materialize: keeping …: …`

The `DerivedStore` given to `Base.KeepDerivedIn` refused to keep a relation `Materialize` derived. The message carries the store's own error. An `Eval` ignores the same failure, since it already has its answer.

`query: graph.closure needs its min and max bound (a variable must appear in a relation before graph.closure reads it)`

An evaluator running the body as written reached a closure whose bounds are variables nothing has bound yet. The planned evaluator moves it after them.

`query: graph.paths needs its mode, min and max bound (a variable must appear in a relation before graph.paths reads it)`

The same for a paths call, whose mode must be bound too.

`query: graph.closure isn't evaluated across versions: ask each version with At`

A shortest path can hold in no one version though each of its steps holds in some, so a closure isn't answered under `Across`, and for the same reason neither is `graph.paths`. Ask `At` each version you need.

## Grouping and ordering

These concern `having`, `order by`, `after`, `limit` and `offset`. A few can only come from a query built in Go, since the parser refuses the text first.

`query: having compares against ?…, which is not a group key — after grouping only a selected variable or a constant is bound`

After grouping, a `having` condition can only compare against a selected variable or a constant.

{{ demo "demos/errors/is-not-a-group-key-after-grouping.yaml" }}

`query: having compares against ?…, which is not a group key — only a selected variable or a constant is bound once the rows are grouped`

The same rule, for a query built in Go.

`query: having compares two aggregates, which is not supported (compare an aggregate against a constant or a group key)`

`having` compares an aggregate with a constant or a group key, not with another aggregate. For a query built in Go.

`query: having compares against an aggregate, which is not supported (compare against a constant or a group key)`

The same, for an aggregate on the right of the comparison. For a query built in Go.

`query: order by …, which is not an answer column (sort on a column the projection selects)`

`order by` names a column the answer has.

{{ demo "demos/errors/which-is-not-an-answer-column.yaml" }}

`query: limit … offset …: a row count can't be negative`

A negative `limit` or `offset`, for a query built in Go.

`query: offset needs an order by, so each page continues where the one before it stopped`

Paging needs an order that stays put. A `limit` alone answers any rows ([#194](https://github.com/panyam/jaala/issues/194)).

{{ demo "demos/errors/offset-needs-an-order-by.yaml" }}

`query: after needs an order by: it continues from the last row in that order`

`after` continues in the `order by`'s order, so it needs one, from the text or from `datalog.After`.

{{ demo "demos/errors/after-needs-an-order-by.yaml" }}

`query: after gives … values for … order by columns`

One value per `order by` column.

{{ demo "demos/errors/after-gives-values.yaml" }}

`query: after needs an order by naming ?… too, or a row tying with the last one would be skipped`

With a plain column left out of the `order by`, two rows could tie on every column it names, and the one after the cursor would be skipped.

{{ demo "demos/errors/after-needs-an-order-by-naming.yaml" }}

`query: After's row has no …, a column the order by reads`

The row passed to `datalog.After` lacks a column the `order by` sorts on. For a query run from Go.

`query: unknown aggregate … (want count/min/max/sum/avg/list)`

The aggregates are `count`, `min`, `max`, `sum`, `avg` and `list`.

{{ demo "demos/errors/unknown-aggregate.yaml" }}

## Stopping

An `Eval` that stops partway returns one of these, and no answer.

`query: evaluation stopped: work passed its budget of … (at …)`

The query did more work than `Budget` allows. A host can test for it with `errors.As` and a `*datalog.BudgetExceeded`.

{{ demo "demos/errors/work-passed-its-budget.yaml" }}

`query: evaluation stopped: …`

The context was cancelled or passed its deadline. It wraps the context's error, so `errors.Is(err, context.Canceled)` works.

`query: evaluation stopped reading …: …`

The context ended while the source was serving a relation.

`query: reading …: …`

The host's source failed to serve a relation. jaala doesn't cache a failed read.

`query: At needs a Source that serves versions (ns.TimedSource)`

`At` was given to a query over a base whose source serves one version. See [versions]({{.Site.PathPrefix}}/guide/versions/).

`query: Diff needs a Source that serves versions (ns.TimedSource)`

`Base.Diff` was called on a base whose source serves one version.

`query: version … is not one the Source serves`

`At` or `Diff` named a version outside the timed source's `Versions()`.

`query: version …: …`

The timed source serves each version as a source of its own (`ns.SourceAt`), and version `…`'s failed: its `SourceAt` returned an error, or the source it returned doesn't serve a relation the vocabulary holds, at its arity (the rest of the message is what `NewBase` says). A failed version isn't kept, so the next query at it asks again.

`query: Across needs a Source that serves versions (ns.TimedSource)`

`Across` was given to a query over a base whose source serves one version.

`query: Across needs at least one version`

`Across` was given an empty set of versions.

`query: Across names versions the Source doesn't serve: …`

Some versions given to `Across` are outside the timed source's `Versions()`; the message lists them as ranges.

`query: At and Across can't both be given`

A query runs at one version or across several, not both.

`query: evaluation stopped looking up …: …`

The context ended while a source that looks facts up (`ns.LookupSource`) was answering a call.

`query: looking up …: …`

The host's source failed to look up a relation's facts for a call with bound arguments. jaala doesn't keep a failed lookup.

`query: looking up …: a batch of … lookups answered …`

A source that answers lookups in batches (`ns.BatchLookupSource`) returned a different number of results than the values it was asked for. It must return one result per value, in the order asked.

`query: evaluation stopped pushing …: …`

The context ended while a source that evaluates conjunctions (`ns.PushSource`) was answering one. The message names the conjunction's literals.

`query: pushing …: …`

The host's source failed to evaluate a conjunction of a body's literals that it agreed to push. jaala doesn't keep a failed push.

`query: pushing …: a row of … tuples for … literals`

A source that evaluates conjunctions returned a row whose tuple count isn't the number of literals it was asked about. Each row holds one tuple per literal, in the order asked.

`query: pushing …: a row's … tuple has … values, want …`

A row's tuple for one relation has a different number of values than the relation takes.

`query: summarizing …: a row's values don't match the summary asked for`

A source that summarizes a goal (`ns.SummarySource`) returned a row whose group or values don't match the variables it was asked for, or a negative match count.

## Setting up a vocabulary

These come from a host's Go code registering a vocabulary, never from a query. They're listed so a host author can find them.

`query: language … is already registered`

`AddLanguage` was called twice for one language.

`query: module … is written in …, and no language of that name is registered`

A module is written in a language the vocabulary doesn't have. Add the language first.

`query: … is a …, not a module`

`Lookup` or a path treats a member as a module.

`query: source lists relation … but serves no schema for it`

The host's source lists a relation it can't describe.

`query: predicate … needs exactly one of Holds and Gen`

A predicate is either a test (`Holds`) or a generator (`Gen`).

`query: filter … needs arity >= 1`

A test takes at least one argument.

`query: generator … declares no Modes; list the binding patterns it accepts (an all-false mode if it may enumerate with nothing bound)`

A generator lists the patterns of bound arguments it accepts.

`query: generator … has a mode of … positions, want …`

A generator's mode has one entry per argument.

`query: … is defined twice, as a … and as a …`

Two registrations claim one path.

`query: … cannot be a …: it is already a module holding …`

A registration claims a path that is already a module.

`query: … needs … to be a module, but it is a …`

A path needs its parent to be a module, and something else is there.

`query: empty path`

A registration with no path.

`query: path … has an empty segment`

A path with an empty part, like `a..b`.

`query: path … holds a character a query cannot spell`

A path a query couldn't name.

`query: federation: member … has no Source`

A member passed to `ns.NewFederation` has a nil `Source`. The member is named by its `Name`, or by its position (`#2`) when it has none.

`query: federation: member … lists … but has no schema for it`

A member's source lists a relation it can't describe.

`query: federation: … has a different schema in member … than in member …`

Two members serve one relation with different arities, labels or types. The engine would read their facts as one relation, so they have to agree. `Doc` may differ.

`query: federation: no member serves versions (ns.TimedSource)`

`Federation.Timed` was called on a federation none of whose members is a timed source, so it has no versions to serve.

`query: … is the engine's own, which a query calls whatever the vocabulary holds: register yours under another name`

`graph.closure` and `graph.paths` are answered by the engine before the vocabulary is asked, so a predicate registered under either name would never be called.

## Internal

These shouldn't reach anyone. If one does, please report it.

`query: internal: negated literal reached the positive solver`

Should never happen. If you see it, it's a bug in jaala; please report it.

`query: internal: … emitted … values for … arguments`

A host generator emitted a row with the wrong number of values.

`query: stop`

Used inside the engine to stop a generator early. It never reaches a host.

`query: closure found its target`, `query: closure found a fact`, `query: internal: an engine walk is run by the engine`, `query: internal: graph.closure found no path from … to …`

Used inside the engine while a closure or a paths call walks: the first two stop a walk at a bound target and a read at its first fact, and the others mean a bug in jaala.
