# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. v0.1.70 (with frontends/nlmodels/v0.1.70 and the first frontends/mcp/v0.1.70)
  carries #350 (#344: `nl.Pack`, `nl.DataCard`, `Try.Kind` `answer`) and #351 (#347:
  `frontends/mcp`, on mcpkit v0.8.2). v0.1.69 carried #350 alone.
- Mission #322 (`mission_agent_frontend`) is active, 15/16 jaala-side; nothing ready in jaala. What's
  left is Declaire's: declaire#302 (its translator and server as a pack on these), declaire#308
  (wording, which replaces `mcp.Instructions`' default), then declaire#290's arm. Exercise logged at
  e77029a (`TestEval` 7/8, `frontends/mcp`'s tests pass).
- Mission #356 (`mission_cypher_frontend`) is active, 0/5, started 2026-10-18 from epic #278.
  Branch `cypher/compile` is #352's first PR (the compiler, the `Graph` view, `TestEval` 7/8);
  its second (WITH, UNION, CALL, EXISTS/COUNT subqueries, the docs page) closes #352, which
  unblocks #353, #354 and #355. #359 also makes `frontends/nl` and `frontends/cypher` modules of
  their own, so its release is three steps (CLAUDE.md, Releasing): jaala alone, then jaala with
  `frontends/nl`, then everything, with `make bump-root` between. Declaire then moves to the nl
  module beside a jaala past v0.1.70 (an ambiguous import otherwise); that upgrade note is owed.
  Its consumer half is declaire#248 (a Cypher arm beside each Datalog arm). No exercise logged yet.
- Waiting: #327 until a host times `Across` over a store; #288 (sessions) after #287 and agni#1027.
- This run: merged #350 and #351, closed #344 and #347; dropped nl/domain-pack and mcp/frontend.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
