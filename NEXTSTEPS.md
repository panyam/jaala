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
- Mission #420 (`mission_graph_server_stores`) is worked in this clone, not `mission:active`. #411
  (`stores/bolt`) is its first ticket and makes `make exercise-graph-server` runnable; next ready is
  #412.
- No open branches in this clone. v0.1.78 carries #392 (PR #394): an `IN` list's walk starts
  from its values, and the planner seeds a constant-bound derived call first. Declaire reruns its
  Cypher-only arm's x10b on it, the ticket's second done-when.
- In other sessions: #393 (its first slice merged in PR #395, the ticket still open), and #280 to #283 (paths,
  bounded and shortest reach, optional matches, lists and maps) in a parallel worktree. Start none of
  them here.
- Mission #356 (`mission_cypher_frontend`) is active, 13/17; its four open tickets are #280 to #283,
  all in the parallel worktree. Exercise logged at be43156 (`TestEval` 7/8, 6
  first try).
- Mission #322 (`mission_agent_frontend`) is active, 18/20; next is #393 (first slice in PR #395). Exercise last
  logged at 056df2b, 5 PRs merged since.
- Off-mission P2, ready: #287 (writes as a change set) then #288 (session overlay), for agni#1027.
- Selfcheck passed on #394's tree (identical to be43156), FuzzCompile clean for 10m on a rerun.
- This run: pruned the stale v0.1.76 lines, #377, #284 and #354 (all closed).

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
