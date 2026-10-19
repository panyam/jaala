---
title: "Aggregation"
description: "count, sum and list over groups, filtering groups with having, and an aggregate in a rule's head."
prev: {url: "/jaala/tutorials/03-negation/", title: "Negation"}
next: {url: "/jaala/tutorials/05-go-host/", title: "Embedding jaala in Go"}
---

An aggregate in the projection reduces many rows to one per group. The plain variables in the projection are the group, and the aggregate is computed over each group's rows. This counts what each package imports:

{{ demo "demos/tutorials/04-fan-out.yaml" }}

`util` has no row because it imports nothing, so there's no group for it, which is worth remembering when a count you expected to be zero is just missing. Several aggregates can share a group, which is a fairly common shape of question. Here's each team's package count and total lines:

{{ demo "demos/tutorials/04-by-team.yaml" }}

The aggregates are `count`, `sum`, `min`, `max` and `list`.

## Counting rows or values

`count(?t)` counts the rows in a group, and `count(distinct ?p)` counts the different values. They differ when a value repeats. The security team has two tests, both of `auth`:

{{ demo "demos/tutorials/04-distinct.yaml" }}

`list` gathers a group's values into a list, in the answer's usual order, which a later query can read again with the `list.*` functions ([reference]({{.Site.PathPrefix}}/reference/builtins/#lists)):

{{ demo "demos/tutorials/04-list.yaml" }}

## Filtering groups

A comparison in the goal filters rows before they're grouped. To filter the groups themselves, by what an aggregate came to, add `having` after the projection. These are the packages at least three others import:

{{ demo "demos/tutorials/04-having.yaml" }}

## An aggregate in a rule

A rule's head can aggregate, which gives the result a name that other rules and goals can use like any relation. `fan_in` is how many packages import each one, and the goal then reads it with an ordinary comparison:

{{ demo "demos/tutorials/04-head-aggregate.yaml" }}

A rule that aggregates has to be its relation's only rule. Two rules each producing a count for the same package would leave it with two counts, and it's not clear which one you'd want. jaala derives everything the rule's body reads before it computes the aggregate, the same way it handles `not`, so the count is never taken over a relation that's still growing.

## A group with no rows

With no plain variables in the projection, the whole answer is one group, and an aggregate over no rows still answers. `legacy` imports nothing the security team owns, so the count is zero rather than missing:

{{ demo "demos/tutorials/04-empty-group.yaml" }}

That's the end of the language tutorials. Everything here also works from Go, which the [next tutorial]({{.Site.PathPrefix}}/tutorials/05-go-host/) covers.
