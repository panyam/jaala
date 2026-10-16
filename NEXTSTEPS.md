# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. v0.1.62 (b8b1b50: #314, a `\"` inside a string reads as v0.1.60 read it) follows
  v0.1.61 (#179 to #181 timed facts, #167 the tokenizer). Declaire hit #314 on v0.1.61 and worked
  around it (declaire#284), so it needs no change to take v0.1.62.
- No active mission, and nothing ready: every open issue is `waiting` or an epic (#183 timed facts,
  #213 stores, #278 Cypher/GQL gaps). Next: `retriage` to start a mission when a host files a need;
  #312 (timed facts through lookups, pushdown, federations) unparks when a host serves versions from
  a store.
- This run: #311, #313, #315 and #316 merged; dropped feat/tokenizer-167 and the v0.1.60 notes.

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- The old parser stays as test code (`datalog/slicer_test.go`) until a grammar change should make it
  disagree; CLAUDE.md's tokenizer bullet says how to retire it.
