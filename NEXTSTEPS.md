# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. Merged since v0.1.52: #271 (#270), #272 (#215), #277 (#206), #274 (#273, with
  #275's #209 inside it). Next: tag them, then `./selfcheck.sh` on the tag. #270 needs an upgrade
  note for Declaire (no code change: q09r on the stores engine should push `http.route` with
  `Inputs`).
- Both active missions have every jaala ticket closed (#219 7/7, #235 14/14 with #270) and wait on
  Declaire's run: bump past v0.1.52 and run `bench -engine stores -time` on the partitioned
  kubernetes view against declaire#180's baseline. #235's exercise last logged at bb965ca; 4 PRs
  merged since.
- New P1 off-mission: #276 (grammar: the clause endings agents write), filed by the owner from
  Declaire's agent exercise; it serves no mission yet, so `retriage` should link or re-rank it.
- Left for the owner: #179 (gated on hosts measuring its premise), #205 (waits for a host serving
  prefixed values), #220 (API shape undecided).
- This run: pruned feat/full-rule-demand, fix/filter-head-215, feat/head-projection-arith-206,
  fix/inverse-mode-zero, feat/corpus-arithmetic-209 (all merged).

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #260 if
  `bindArg`/`binding.clone` dominate (jaala's own workloads say they do, 63% of allocation, posted
  on #260 and #234), #222 and #234 on their triggers.
