---
title: "Guide"
description: "The rules of jaala's query language, one topic per page, each shown by an example you can edit."
---

The [tutorials]({{.Site.PathPrefix}}/tutorials/) teach the language in order. These pages are where you look things up afterwards, and each one covers a single topic in full, including what jaala refuses and why. Like everything on this site, every example runs on jaala when the site is built, and you can edit and rerun it in the page. They all use the same [fact set]({{ datasetURL "deps" }}) as the tutorials.

1. [Syntax](syntax/) lists every form a query can take.
2. [Types and constants](types-and-constants/) covers how a constant is read, and how numbers and text compare.
3. [Arithmetic and units](arithmetic/) covers expressions, functions that run in any direction, and units; its examples bring their own resistors rather than the shared fact set.
4. [Negation](negation/) covers anchoring, existential variables, host-bound values and stratification.
5. [Aggregation](aggregation/) covers groups, rows against values, `having`, and what an aggregate answers over nothing.
6. [Ordering](ordering/) covers the default order, `order by`, `limit` and `offset`.
7. [Host options](host-options/) covers `Bind`, `Budget`, `Explain` and the context, pages and tables, sharing a base, and where a parse error is, from Go.
8. [Predicates and generators](predicates/) covers a host's own Go inside a query: filters, generators, their modes and types.
9. [Facts from stores](stores/) covers a source that answers from its own index: lookups, federations, and stores that join, count, page and walk.
10. [Modules and the vocabulary](modules-and-vocabulary/) covers paths, private members, docs, signatures and module errors.
11. [Absent values](absent-values/) covers a field the source didn't state.
12. [Versions](versions/) covers a source serving several versions of its facts, `At`, `Across` and `Diff`, from memory or from stores.

The last six are written in Go, as `Example` functions that `go test` runs, like the [Go tutorial]({{.Site.PathPrefix}}/tutorials/05-go-host/).
