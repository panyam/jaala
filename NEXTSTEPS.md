# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs here. `main` is edeef61: v0.1.103 plus #558, #559 (every module on
  `go 1.27.4`), #561 (`nlmodels/memory`, chakra v0.3.6) and #562. Unreleased: the next tag needs an
  upgrade note that hosts need Go 1.27.4 or newer.
- Mission #437 (`mission_text_memory`) is 3/4. #415 is open for the LongMemEval pilot: download
  `longmemeval_oracle.json`, set `LONGMEMEVAL_DATA_PATH`, then run `TestPilot` (see #561's "Try it";
  paid calls). Its failures decide the tickets for dates, a similarity contract and the recall audit.
- Mission #322 (`mission_agent_frontend`) is 49/51 and waits on Declaire's gpt-5.5 arms (declaire#300,
  declaire#371). #547 is parked `waiting`. #544 (P2) is next in the queue, but its evidence is all
  `14b:loop`, which frontier-first parks.
- Selfcheck: passed on 0d0a16b (2026-11-24). Check `make selfcheck-status` before the next merge.
- This run: dropped memory/415-longmemeval (PR #561 merged).

## Across threads

- The agent-frontend order is frontier first (the user, 2026-11-09): beat Codex on gpt-5.5, then cut
  cost, then change model. Park small-model tuning until then.
