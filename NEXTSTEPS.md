# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches or PRs here. `main` is 38313f8: v0.1.104 plus #564 to #578 (the loop after a
  refused draft: #566, #568, #570, #576; the Scorer #577; per-tag cost and `RouteFor` #578; the
  memory's held keys #565). Unreleased: the next tag needs an upgrade note that hosts need Go 1.27.4
  or newer.
- Mission #322 (`mission_agent_frontend`) is 56/57; only #547 (`waiting`) is open. Declaire's
  `14b` and `14b:loop` arms stay at 2/15 through four loop PRs (logs on #322). Next is Declaire's:
  a `local-then-frontier` arm with a scope Scorer (declaire#300), and `RouteFor` live (#574's arm).
  Declaire's Grounder fixes for x06 and x09 are declaire#389.
- Mission #437 (`mission_text_memory`) is 4/5. #415 is next: arm 4 (`MemoryArm`, `read_session`)
  and an S-size run (paid calls; `TestPilot` as #561's "Try it" says). chakra#343 is still open.
- #573 (a ladder of N tiers) is `waiting` for a third model that earns a rung.
- Selfcheck: passed on 45b131a (2026-11-25). Check `make selfcheck-status` before the next merge.
- This run: merged #566, #568, #570, #576, #577, #578; no threads left.

## Across threads

- The agent-frontend order is frontier first (the user, 2026-11-09): beat Codex on gpt-5.5, then cut
  cost, then change model. Park small-model tuning until then.
