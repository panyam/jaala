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

## The goal and its projection

After the goal, `=>` and a list of columns say what the answer holds. The columns are variables, [aggregates]({{.Site.PathPrefix}}/guide/aggregation/) or [arithmetic]({{.Site.PathPrefix}}/guide/arithmetic/#in-the-answer-and-in-a-rules-head), and after them can come `having`, `order by`, `limit` and `offset`, in that order. Leave out `=>` and its columns, and the answer has one column per variable of the goal, in the order they first appear:

{{ demo "demos/guide/syntax-no-projection.yaml" }}

## Rules

A rule is a head, `:-`, and a body: `depends_on(?a, ?b) :- imports(?a, ?b);`. Each argument of the head is a variable the body binds, a constant, an [aggregate]({{.Site.PathPrefix}}/guide/aggregation/#an-aggregate-in-a-rule), or [arithmetic]({{.Site.PathPrefix}}/guide/arithmetic/#in-the-answer-and-in-a-rules-head) over what the body binds, and a variable can declare its type, as in `size(?p, ?n: number)`, which the [next page]({{.Site.PathPrefix}}/guide/types-and-constants/) covers. A relation can have several rules, and its tuples are everything any of them derives. A relation name can contain dots and hyphens, which is how jaala names the standard library's tests (`str.contains`) and a module's members (`net.has_test_point`).

## Endings from other Datalogs

jaala also reads the endings Datalog, Prolog and Soufflé write, so a program written in their habits runs as its jaala spelling does. A `.` followed by a space or the end ends a clause, as `;` does; a `.` inside `str.suffix` or `1.5` doesn't. A goal written `?- r(?a)` or `=> r(?a)` asks `r(?a)` with no projection, and `!atom` is `not atom`:

{{ demo "demos/guide/syntax-other-endings.yaml" }}

A variable is still `?a`. A capitalised word, as Prolog writes a variable, is refused rather than read as one, because `pin(?r, GND)` meant as a constant would otherwise match every net.
