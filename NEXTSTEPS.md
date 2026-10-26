# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs. `main` is v0.1.88, which ships #475 (#474, `mcp.Options.Server`, for
  agni#1172's `WithListTTLMs`); agni takes it from the tag.
- Mission #322 (`mission_agent_frontend`), the one active mission, has all 26 jaala tickets closed and
  nothing ready. It closes when Declaire's translator run meets declaire#288's bar. Its exercise last
  ran at f82be84, and 7 PRs have merged since, three of them mcp (#470, #473, #475): rerun it before
  calling the mission done.
- Mission #437 (`mission_text_memory`, not active) is 2/3. Next ready is #415, which needs #437 made
  active first (`retriage`).
- Mission #420 (Neo4j/Memgraph, not active) is 4/6, both left with hosts (declaire#339, agni#1073).
- Selfcheck: last pass d15c542; 5 PRs merged since, so one was started on v0.1.88's commit at this
  checkpoint (`make selfcheck-status` reads its note).
- This run: dropped feat/mcp-server-options (PR #475 merged); the closed #452's branch is gone from the
  remote.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
