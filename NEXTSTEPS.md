# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs. `main` is 8a478a0: v0.1.103 plus #558, which runs vulncheck through
  `panyam/ci-actions` v0.1.1 (#553). The old step piped the scan into `tee` without pipefail, so it
  never failed on a finding.
- #557 (P1, bug): vulncheck is red on `main` from three standard-library advisories. The fix is
  raising the modules' `go` lines (nlmodels, mcp, docsite on 1.26.9; root, nl, cypher, bolt on
  1.26.4) past go1.26.11. Next up, ahead of the missions.
- Mission #322 (`mission_agent_frontend`) is 49/51 and waits on Declaire's gpt-5.5 arms (declaire#300,
  declaire#371). #547 is parked `waiting`. #544 (P2) is next in the queue, but its evidence is all
  `14b:loop`, which frontier-first parks.
- Mission #437 (`mission_text_memory`) is 3/4; next ready is #415 (P1), unblocked now that
  panyam/chakra#246 is closed. Its exercise was last logged at d56ca11, and 31 PRs have merged since.
- Selfcheck: passed on 0d0a16b (2026-11-24); 1 PR merged since.
- This run: closed out #553 (PR #558 merged).

## Across threads

- The agent-frontend order is frontier first (the user, 2026-11-09): beat Codex on gpt-5.5, then cut
  cost, then change model. Park small-model tuning until then.
