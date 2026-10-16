# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. `main` holds #328 (#317: `Across` looks a bound call up through an
  `ns.TimedLookupSource`; #319: a recursion through a renaming helper walks) since v0.1.63. Next:
  tag v0.1.64 after the CI set and `./selfcheck.sh` on `main`, with the upgrade note on #317 (a
  rename helper now shows `inlined` in `Explain`).
- No active mission, and nothing ready. Waiting: #327 (`Across` kept between Evals, and batches,
  pushes, walks and streams across versions) until a host times `Across` over a store; #288
  (session overlays) after #287 and agni#1027. Next: `retriage` to start a mission around agni
  serving revisions from stores (agni#971, #999, #1007) once agni moves past v0.1.62.
- This run: tagged v0.1.63; merged #328, closing #317 and #319; filed #327.

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- The old parser stays as test code (`datalog/slicer_test.go`) until a grammar change should make it
  disagree; CLAUDE.md's tokenizer bullet says how to retire it.
