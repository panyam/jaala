# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. v0.1.76 is being tagged in one go (#375) from `make bump-root V=v0.1.76`;
  Declaire's `Store.Derived` upgrade note goes on #378 once it resolves.
- Mission #356 (`mission_cypher_frontend`) is active, 6/9; next ready #354 (`nl.Translate` writing
  Cypher, the 32B arm) and #377 (P1, a chain of XORs that never finishes compiling); consumer half
  declaire#248. Exercise logged at 2f9fc88 (`TestEval` 7/8).
- Mission #322 (`mission_agent_frontend`) is active, 18/19; its last ticket is #354. Exercise
  logged at f7a4d72 (`TestEval` 7/8).
- Off-mission P2, ready: #287 (writes as a change set) then #288 (session overlay), for
  agni#1027; #284's remainder (avg, percentiles, coalesce) still undecided: next PR, or park.
- Waiting: #327 until a host times `Across` over a store; #364 until a Cypher case needs `CALL { }`
  or an order or limit inside a stage.
- This run: #378 (PR #380), #370 (#379) and #381 merged; selfcheck on 2f9fc88 still running.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
