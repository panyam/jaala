---
title: "Error messages"
description: "Every error jaala can return, what causes it, and an example of each one a query can trigger."
---

Every error jaala returns starts with `query:`, so a host can tell a mistake in a question from a failure elsewhere. The wording of each message stays stable once it ships, since hosts match on fragments of it, and a new case gets new wording rather than moving an old one. This page lists every message in jaala's source, which a test checks when the site is built, so a message added to the engine fails the docs build until it's listed here.

In each message, `…` stands for the part that names your relation, variable or value. Where a query can trigger it, an example follows, pinned to the message's wording, and you can edit it in the page.

## Reading the query

The parser reads the text before anything else runs, so these come first and say where the text went wrong.

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

`query: empty query`

A query with no text at all, from a host that built one empty.

`query: malformed atom … (want reln(args))`

An atom is a relation name and its arguments in parentheses. An unclosed string or parenthesis usually causes this.

{{ demo "demos/errors/malformed-atom.yaml" }}

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

A string is missing its closing `"`.

{{ demo "demos/errors/unterminated-string.yaml" }}

`query: bare identifier … — a term must be a ?variable, a "string", or a number`

A term is a `?variable`, a quoted string or a number. A word without quotes is none of them.

{{ demo "demos/errors/bare-identifier.yaml" }}

`query: projection column … must be a ?variable or an aggregate`

The columns after `=>` are variables or aggregates.

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

`query: … comes after …, not before it (the order is having, order by, limit, offset)`

The clauses after the projection come in a fixed order.

{{ demo "demos/errors/comes-after-s-not-before-it.yaml" }}

`query: order needs by, as in …`

It is `order by`.

{{ demo "demos/errors/order-needs-by.yaml" }}

`query: order by needs a column, as in …`

`order by` names at least one column.

{{ demo "demos/errors/order-by-needs-a-column.yaml" }}

`query: order by: …`

Wraps an error in one of `order by`'s columns, with that error's own text.

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

A name the vocabulary doesn't hold. When a close name exists, the message suggests it.

{{ demo "demos/errors/unknown-relation-q-s.yaml" }}

`query: … is a module, not a relation; it holds …`

A module's path used as a relation. The message lists what the module holds.

{{ demo "demos/errors/is-a-module-not-a-relation-it-holds.yaml" }}

`query: unknown module … in …`

The first part of a path names no module, which is a different typo from a wrong last part.

{{ demo "demos/errors/unknown-module-q-in-q.yaml" }}

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

## Running the query

These depend on the order variables get their values, so jaala checks them against the body as it will run.

`query: comparison operand is unbound (a variable must appear in a relation before it is compared)`

A comparison needs both sides to have values, so a variable it compares has to appear in an atom of the same goal or rule body. Where the comparison is written doesn't matter: it's checked once that atom has bound the variable. In a rule, this is refused before the query runs.

{{ demo "demos/errors/comparison-operand-is-unbound.yaml" }}

`query: … needs all arguments bound (a variable must appear in a relation before … tests it)`

A test like `str.contains` needs every argument bound by the rest of the goal.

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

`query: cannot bind …: the goal does not use it`

`Bind` names a variable the goal doesn't use, usually a typo.

{{ demo "demos/errors/cannot-bind-s-the-goal-does-not-use-it.yaml" }}

`query: invalid regex …: …`

A pattern given to `str.match` doesn't compile. The message carries the compiler's reason.

{{ demo "demos/errors/invalid-regex.yaml" }}

`query: invalid glob …: …`

A glob given to `str.glob` doesn't compile.

{{ demo "demos/errors/invalid-glob.yaml" }}

## Grouping and ordering

These concern `having`, `order by`, `limit` and `offset`. A few can only come from a query built in Go, since the parser refuses the text first.

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

`query: unknown aggregate … (want count/min/max/sum/list)`

The aggregates are `count`, `min`, `max`, `sum` and `list`.

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

`query: evaluation stopped looking up …: …`

The context ended while a source that looks facts up (`ns.LookupSource`) was answering a call.

`query: looking up …: …`

The host's source failed to look up a relation's facts for a call with bound arguments. jaala doesn't keep a failed lookup.

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

## Internal

These shouldn't reach anyone. If one does, please report it.

`query: internal: negated literal reached the positive solver`

Should never happen. If you see it, it's a bug in jaala; please report it.

`query: internal: … emitted … values for … arguments`

A host generator emitted a row with the wrong number of values.

`query: stop`

Used inside the engine to stop a generator early. It never reaches a host.
