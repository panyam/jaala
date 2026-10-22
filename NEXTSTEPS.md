# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs. `main` is at abd61a6, v0.1.87, which ships #471 (#444, `nl.Extract`'s quoted
  keys) and #470/#473 (mcp).
- Mission #322 (`mission_agent_frontend`), the one active mission, has all 26 jaala tickets closed and
  nothing ready. It closes when Declaire's translator run meets declaire#288's bar. Its exercise last
  ran at f82be84, and 6 PRs have merged since, two of them mcp (#470, #473): rerun it before calling the
  mission done.
- Mission #437 (`mission_text_memory`, not active) is 2/3 after #444. Next ready is #415 (LongMemEval as a
  lens on missing primitives), which needs #437 made active first (`retriage`). #437's log at d56ca11:
  `TestExtract` works; the LongMemEval half can't run until #415 adds it.
- Mission #420 (Neo4j/Memgraph, not active) is 4/6, both left with hosts (declaire#339, agni#1073).
- Selfcheck: last pass d15c542; 4 PRs merged since (#468, #470, #471, #473), due at 5.
- This run: dropped the stale v0.1.84 release line; `origin/cypher/rel-vars-rest-445` (PR #452, closed
  as #451's duplicate) is the one unmerged remote branch, safe to delete.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
