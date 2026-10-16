# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. `main` is ba12c65: jaala v0.1.66 (ab16caa) and frontends/nlmodels/v0.1.66
  (1f28f76) ship `frontends/nl` and its clients at their new paths, noted on declaire#290. The next
  release is `make tag-push V=v0.1.67 NOTES=notes.txt` (#339).
- Mission #322 (`mission_agent_frontend`) is active, 9 of 10 tickets closed: every jaala ticket is
  done, and it stays open for Declaire's translator-alone arm (declaire#290). Nothing ready in
  jaala. Exercise last ran at ab16caa (`go test ./frontends/nl -run TestEval -v`: 7/8, 2 first
  try, fixes 6/6); one PR (#339, tooling only) merged since.
- Waiting: #327 (`Across` kept between Evals, and batched, pushed, walked and streamed across
  versions) until a host times `Across` over a store; #288 (sessions) after #287 and agni#1027.
- This run: merged #333, #335, #338 and #339 (#323, #324, #330, #336, #337); tagged v0.1.66 and
  frontends/nlmodels/v0.1.66. Merged remote branches still hold old NEXTSTEPS copies with no threads.

## Across threads

- `make setup` installs Soufflé 2.5 and `make llama` llama.cpp's `llama-server` and its 1 MB test
  model, so `./selfcheck.sh` runs both steps locally; a selfcheck worktree needs its own copy of
  `frontends/nlmodels/testdata/stories260K.gguf`.
- The old parser stays as test code (`datalog/slicer_test.go`) until a grammar change should make it
  disagree; CLAUDE.md's tokenizer bullet says how to retire it.
