---
title: "Architecture"
description: "How jaala is put together: its packages, how it evaluates a query, the rewrites that make it fast, what it asks a host's stores, where an answer's evidence comes from, and how it checks itself."
---

These pages explain how jaala works inside, for someone extending it, embedding it deeply, or deciding whether to trust it. The [tutorials]({{.Site.PathPrefix}}/tutorials/) and the [guide]({{.Site.PathPrefix}}/guide/) cover using it, and you won't need these pages for that.

1. [Layering](layering/) covers the three packages and why each imports only the one below it.
2. [Evaluation](evaluation/) follows a query from text to answer: linking, checking, strata, and the fixpoint.
3. [Rewrites](rewrites/) covers what the planned evaluator does to a query before running it, and when each step stands aside.
4. [Stores](stores/) covers what the planned evaluator asks a host's store, where the answers are kept, and why a store's precision costs calls but never answers.
5. [Provenance](provenance/) covers where an answer's citations and witnesses come from.
6. [Testing](testing/) covers how jaala checks its own answers, its own cost, and these docs.

Claims about behaviour here are shown by examples that run when the site is built, like everywhere else on the site.
