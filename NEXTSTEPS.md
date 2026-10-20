# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs in this clone. v0.1.84 (on `main` after #451) carries #445's relationship
  variables through WITH, in subqueries and on a path; Declaire's frontier Cypher arm can rerun x10b on it.
- Mission #322 (`mission_agent_frontend`) is active, 22/22 jaala tickets closed; it waits on Declaire's
  translator run against declaire#288's bar. Exercise last logged at fbfa0ff; 10 PRs merged since.
- Mission #356 (`mission_cypher_frontend`) is 24/24 closed and every done-when line holds (log on #356 at
  d4e5187, exercise at e37db85); another session is closing it. #453 (P3, `waiting`) holds the two
  subquery edges #452's tests found in #451.
- Mission #420 (Neo4j/Memgraph, not `mission:active`) is 4/6. Mission #437 (`mission_text_memory`,
  not active) is 1/3, next ready #415.
- Selfcheck: 36f59f2 recorded FAIL, a corpus timeout at a load near 80 (passed rerun alone, 18m). A run
  on e37db85 was going at checkpoint; `make selfcheck-status` shows whether it recorded a pass.
- This run: no threads on any branch; rewrote At a glance (dropped v0.1.82 and the #402 run, superseded).

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
