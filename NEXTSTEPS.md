# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. `main` is d5ef070: jaala v0.1.65 (2fc0af0) and nlmodels/v0.1.65 (d5ef070) ship
  the `nl` front end and its model clients, upgrade note on #322.
- Mission #322 (`mission_agent_frontend`) is active, 7 of 10 linked tickets closed. Ready, both P1:
  #336, a `Fix` for an unknown name with nothing close (the 3 of `TestEval`'s 6 check errors without
  one), and #337, moving `nl` and `nlmodels` under `frontends/`. #337 was filed before v0.1.65 and
  nlmodels/v0.1.65 were tagged, so the move now changes the paths those tags ship (hence its
  "retract the old nlmodels path"): tell declaire#290 before it builds on `github.com/panyam/jaala/nl`.
  Exercise last ran at 2fc0af0 (`go test ./nl -run TestEval -v`: 7/8 passed, 2 first try, fix 3/6);
  the real one is declaire#290's translator-alone arm, which is Declaire's.
- Waiting: #327 (`Across` kept between Evals, and batched, pushed, walked and streamed across
  versions) until a host times `Across` over a store; #288 (sessions) after #287 and agni#1027.
- This run: merged #332 (#325), #334 (#326, #331); tagged v0.1.65 and nlmodels/v0.1.65; filed #336;
  picked up #337 (another session's); dropped the merged `feat/nl-*` and `feat/grammar-323` branches.

## Across threads

- `make setup` installs Soufflé 2.5 and `make llama` llama.cpp's `llama-server` and its 1 MB test
  model, so `./selfcheck.sh` runs both steps locally; copy the model into a selfcheck worktree.
- The old parser stays as test code (`datalog/slicer_test.go`) until a grammar change should make it
  disagree; CLAUDE.md's tokenizer bullet says how to retire it.
