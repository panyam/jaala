# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches in this clone. Since v0.1.78, `main` carries agni#1027's engine half: `ns.Changes`
  (#287, PR #406), `ns.Overlay` with `TimedMemSource.Commit` (#288, PR #410) and the change set's
  JSON with `ns.Session` (#417, PR #418). No tag carries them yet; agni needs one to start.
- Mission #356 (`mission_cypher_frontend`) is active, 18/19; next ready is #407 (`graph.paths`'
  planned error differs from Naive's on an unreached rule; selfcheck's `FuzzEval` keeps finding it,
  so every selfcheck fails on it until it's fixed).
- Mission #322 (`mission_agent_frontend`) is active, 18/20; next is #393. #420 (Neo4j/Memgraph) is
  open but not active, next ready #411.
- Both exercises were last logged at d1c5c4e, and 12 PRs have merged since: rerun them before the next
  log comment.
- Hygiene: #402 is P1 and serves no mission (Declaire's x05 cross product). #414 is a P2 orphan.
- Waiting: #404 (write statements, which compile into a change set). Not filed: a multi-player
  commit from the latest version that reports facts two sessions both changed, until a second editor
  appears.
- This run: dropped the v0.1.78 and #392 lines, and #280 to #283 (all closed), plus the #287/#288
  line (both merged). No branch carried a thread.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
