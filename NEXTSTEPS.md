# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs. `main` is v0.1.88, which ships #475 (#474, `mcp.Options.Server`, for
  agni#1172's `WithListTTLMs`); agni takes it from the tag.
- Mission #437 (`mission_text_memory`) is active from 2026-10-26 and 2/3. Next ready is #415 (P1):
  its first PR is a `make exercise-memory` target running the one-shot arm (`Extract`, then
  `Translate`) on a LongMemEval oracle subset through a local model.
- Mission #322 (`mission_agent_frontend`), also active, has all 26 jaala tickets closed and nothing
  ready; it closes when Declaire's translator run meets declaire#288's bar. Exercise rerun at 02aa5de
  (v0.1.88): nl and Cypher `TestEval` both 7/8, unchanged.
- Mission #420 (Neo4j/Memgraph, not active) is 4/6, both left with hosts (declaire#339, agni#1073).
- Selfcheck: passed on 7079085 (v0.1.88), 5000 seeds; next due 5 merged PRs after it.
- This run: dropped feat/mcp-server-options (PR #475 merged); the closed #452's branch is gone from the
  remote.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
