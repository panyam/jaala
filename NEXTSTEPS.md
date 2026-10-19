# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches here. v0.1.76 is tagged (every module in one `make tag-push`, #375); Declaire's
  `Store.Derived` upgrade note goes on #378.
- In another session: #377 (P1, a chain of XORs that never finishes compiling) and #284's remainder
  (avg, percentiles, coalesce). Don't start either here.
- Mission #356 (`mission_cypher_frontend`) is active, 6/9; next ready here is #354 (`nl.Translate`
  writing Cypher, the 32B arm, which also took #353's did-you-mean label); consumer half
  declaire#248. Exercise logged at 2f9fc88 (`TestEval` 7/8).
- Mission #322 (`mission_agent_frontend`) is active, 18/19; its last ticket is #354. Exercise
  logged at f7a4d72 (`TestEval` 7/8).
- Off-mission P2, ready: #287 (writes as a change set) then #288 (session overlay), for agni#1027.
- Waiting: #327 until a host times `Across` over a store; #364 until a Cypher case needs `CALL { }`
  or an order or limit inside a stage.
- Selfcheck passed on 8a49fb1 (#371 and #372 together), llama.cpp's step included, now that
  `make llama` has fetched the model into the main checkout.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
