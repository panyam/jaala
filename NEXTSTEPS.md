# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. `main` (c8d4564) holds two merges since v0.1.62: #318 (#312, `ns.SourceAt`, a
  version served as a store, and `Federation.Timed`) and #320 (docs: stores, predicates and
  generators, architecture of stores). Unreleased: agni takes tags only, so it can't serve revisions
  from stores until v0.1.63. Next: tag it when the owner says, after the CI set and `./selfcheck.sh`
  on merged `main` (#318 and #320 never ran together).
- No active mission. Ready off-mission: #319 (P3, a recursive rule reading through a one-rule
  helper isn't walked). Waiting: #317 (`Across` through stores, an `Across` cache) until a host
  asks `Across` over a history in stores; #288 (session overlays) after #287 and agni#1027's
  trigger, and it will wrap the per-version Source #318 added. Next: `retriage` to start a mission
  around agni serving revisions from stores (agni#971, #999, #1007) once agni picks up v0.1.63.
- This run: dropped the v0.1.60-era notes; merged #315, #316 (v0.1.62), #318, #320.

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- The old parser stays as test code (`datalog/slicer_test.go`) until a grammar change should make it
  disagree; CLAUDE.md's tokenizer bullet says how to retire it.
