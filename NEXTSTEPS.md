# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs in this clone. v0.1.82 (on 85fb45d, `main`'s head) carries #402 (PR
  #432) and #430 (PR #431); Declaire can rerun x05 as agents write it on that tag.
- Mission #322 (`mission_agent_frontend`) is active, 21/22: every jaala ticket is closed, and the one
  left is declaire#290, Declaire's half. It closes when Declaire's translator run meets declaire#288's
  bar. Exercise logged at fbfa0ff (nl 7/8, Cypher 7/8); 2 PRs merged since.
- Mission #356 (`mission_cypher_frontend`) is active, 19/19 closed; what's left is `waiting` until an
  exercise case is refused for one. Close it when Declaire's Cypher arm (declaire#248) reports.
- Mission #420 (Neo4j/Memgraph, not `mission:active`) is 3/6; next ready #413. PR #429 (#412 part 1)
  merged.
- Selfcheck passed on f007094 (#393's three PRs together). A run on #402's branch tree (e1ef44e) was
  still going at checkpoint; `make selfcheck-status` shows whether it recorded a pass.
- Mission #437 (`mission_text_memory`, not active, from the 2026-10-19 retriage) holds #414 then
  #415; its exercise becomes runnable with #414's eval. #384, #404 and #419 are `waiting` only now.
- This run: rewrote the whole file (it mixed two older runs naming #393, #407 and #411 as next, all
  closed). Most remote branches are merged and can be deleted after `gh pr view` says `MERGED`.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
