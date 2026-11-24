# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs. `main` is bdf8e59: v0.1.103 plus #558 (vulncheck through
  `panyam/ci-actions` v0.1.1) and #559 (every module on `go 1.27.4`, which clears three stdlib
  advisories; vulncheck green on `main`). Unreleased: the next tag needs an upgrade note saying hosts
  need Go 1.27.4 or newer. agni, Declaire and chakra already use it.
- Mission #322 (`mission_agent_frontend`) is 49/51 and waits on Declaire's gpt-5.5 arms (declaire#300,
  declaire#371). #547 is parked `waiting`. #544 (P2) is next in the queue, but its evidence is all
  `14b:loop`, which frontier-first parks.
- Mission #437 (`mission_text_memory`) is 3/4; next ready is #415 (P1), unblocked now that
  panyam/chakra#246 is closed. Its exercise was last logged at d56ca11, and 32 PRs have merged since.
- Selfcheck: passed on 0d0a16b (2026-11-24); 2 PRs merged since.
- This run: closed out #557 (PR #559 merged).

## Across threads

- The agent-frontend order is frontier first (the user, 2026-11-09): beat Codex on gpt-5.5, then cut
  cost, then change model. Park small-model tuning until then.
