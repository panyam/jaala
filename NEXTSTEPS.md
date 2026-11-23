# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs. `main` is v0.1.103 (58df908, CI green): #550 `Role.Reasoning` and
  `Try.Truncated`, #552 `Abstain.Declined`.
- Mission #322 (`mission_agent_frontend`) is 49/51 and waits on Declaire's gpt-5.5 arms (declaire#300,
  declaire#371): chakra's agent driver, Codex, and the translator with `Role.Reasoning` and
  `Abstain.Declined`, all on one model. Exercise at 06e8062: `TestEval` unchanged; `TestEvalOnOpenAI`
  7 passed, 0 wrong, 1 correct decline. #547 (`Actions.Collapse`'s measuring) is parked `waiting` until
  the work reaches the small-model step. #544 (P2) is the next ready jaala ticket.
- Mission #437 (`mission_text_memory`) is 3/4; next ready is #415 (P1). Its exercise was last logged at
  d56ca11, and 30 PRs have merged since.
- Selfcheck: last pass 9bf08e4 (2026-11-09); 5 PRs merged since, so `make selfcheck` is due now.
- This run: rewrote the file from v0.1.91's state; no threads to carry.

## Across threads

- The agent-frontend order is frontier first (the user, 2026-11-09): beat Codex on gpt-5.5, then cut
  cost, then change model. Park small-model tuning until then.
