# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. Next release goes in one go (#375): `make bump-root V=v0.1.76`, commit, `make tag-push`.
- Mission #356 (`mission_cypher_frontend`) is active, 5/7; next ready #354 (`nl.Translate` writing
  Cypher, the 32B arm); consumer half declaire#248. Exercise logged at 8a49fb1 (`TestEval` 7/8).
- Mission #322 (`mission_agent_frontend`) is active, 16/18; next ready #370 (P1), then #354, then
  #361 (P2). Exercise logged at 8a49fb1 (`TestEval` 7/8).
- Off-mission P2, ready: #287 (writes as a change set) then #288 (session overlay), for
  agni#1027; #284's remainder (avg, percentiles, coalesce) still undecided: next PR, or park.
- Waiting: #327 until a host times `Across` over a store; #364 until a Cypher case needs `CALL { }`
  or an order or limit inside a stage.
- This run: dropped cypher/compile (PR #359 merged) and every v0.1.70/v0.1.71 line; #352, #353 and
  #355 closed.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
