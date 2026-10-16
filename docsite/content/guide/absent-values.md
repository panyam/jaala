---
title: "Absent values"
description: "A field the source didn't state is absent, which isn't the same as empty, and how to ask about one."
prev: {url: "/jaala/guide/modules-and-vocabulary/", title: "Modules and the vocabulary"}
next: {url: "/jaala/guide/versions/", title: "Versions"}
---

A source can leave a field out. A datasheet row might state a maximum and no minimum, and a package might declare no license. jaala keeps that apart from a field stated as the empty string, because "not stated" and "stated as nothing" are different facts, and a query that mixed them up would answer the wrong question without saying so. As a host, you serve an absent field as `ns.Absent()`, which is a bit more work than an empty string.

```go
{{ includeFileText "examples/guide/absent_test.go" }}
```

## How an absent value behaves

- It sorts before every number and every piece of text, so in the default [order]({{.Site.PathPrefix}}/guide/ordering/) absent rows come first.
- It isn't equal to `""`. `pkg(?p, "")` finds `log`, which states an empty license, and not `api`, which states none. Grouping keeps them apart too.
- `absent(?x)` holds for an absent value and nothing else, so it's the way to ask for one. `not absent(?x)` reads "this field is stated", which you'll probably want about as often.
- `ValidateBound` checks a query before the host has the values it will bind. It stands an absent value in for each one, which neither the type check nor a closed set of allowed values refuses, so the check judges the query and not a placeholder.
