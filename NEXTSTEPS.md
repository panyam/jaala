# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs here. `main` is c4c0e2e: v0.1.104 plus #564 (the pilot samples each type,
  judged by gpt-4o-2024-08-06) and #565 (Extract lists held keys and refuses a cut reply, chakra
  v0.3.7). Unreleased: the next tag needs an upgrade note that hosts need Go 1.27.4 or newer.
- Mission #437 (`mission_text_memory`) is 4/5. Pilot on c4c0e2e: long context 11/12, jaala one shot
  9/12, with jaala spending more tokens at setup on oracle histories (log on #437). #415 is next: arm 4
  (`MemoryArm`, `read_session`) and an S-size run. Run `TestPilot` as #561's "Try it" says (paid
  calls). panyam/chakra#343 is still open, so the pilot keeps its own judge wrapper.
- Mission #322 (`mission_agent_frontend`) is 49/51 and waits on Declaire's gpt-5.5 arms (declaire#300,
  declaire#371). #547 is parked `waiting`. #544 (P2) is next in the queue, but its evidence is all
  `14b:loop`, which frontier-first parks.
- Selfcheck: passed on 62f70a3 (2026-11-25). Check `make selfcheck-status` before the next merge.
- This run: dropped memory/563-pilot-findings (PR #565 merged).

## Across threads

- The agent-frontend order is frontier first (the user, 2026-11-09): beat Codex on gpt-5.5, then cut
  cost, then change model. Park small-model tuning until then.
