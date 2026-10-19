# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches in this clone. v0.1.78 carries #392 (PR #394): an `IN` list's walk starts
  from its values, and the planner seeds a constant-bound derived call first. Declaire reruns its
  Cypher-only arm's x10b on it, the ticket's second done-when.
- In another session: PR #395 (`nl/action-loop-393`, #393's first slice). Don't start #393 here.
- Mission #356 (`mission_cypher_frontend`) is active, 13/17; next ready #281 (bounded and shortest
  reach). Exercise last logged at b696e78; at be43156 `TestEval` in `frontends/cypher` passes 7/8,
  6 first try, and its log comment waits on the selfcheck below.
- Mission #322 (`mission_agent_frontend`) is active, 18/20; next is #393 (PR #395). Exercise last
  logged at 056df2b, 5 PRs merged since.
- Off-mission P2, ready: #287 (writes as a change set) then #288 (session overlay), for agni#1027.
- Selfcheck on #394's tree (identical to be43156), FuzzCompile 10m, was queued behind another
  session's run when this checkpoint was written.
- This run: pruned the stale v0.1.76 lines, #377, #284 and #354 (all closed).

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
