# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches; everything is merged and released (v0.1.52 on bb965ca, 2026-10-15). Upgrade
  notes are on #264 (v0.1.51, no host code changes) and #268 (v0.1.52, for Declaire's stores).
- Both active missions have every jaala ticket closed and wait on Declaire's run, from a Declaire
  session: bump to v0.1.52, answer `Partial` with `Inputs` on the SQLite stores (declaire#240), and
  run `bench -engine stores -time` on the partitioned kubernetes view against declaire#180's
  baseline. Quoting it on #219 and #235 closes both, or names the next ticket.
- #219 `mission_store_federation`: 7/7, exercise last logged at e31a11f (2026-10-14). #235
  `mission_store_pushdown`: 13/13, logged at bb965ca (2026-10-15, after #269).
- No P0 or P1 is open. Next ready, off-mission: #215 (P3, a small correctness fix); other P3s are
  #179, #205, #206, #209, #220.
- This run: released v0.1.51 (#265 streaming, #267 fuzz fix) and v0.1.52 (#269: #268 and #263,
  folding #259); pruned the three merged branches.

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #260 if
  `bindArg`/`binding.clone` dominate (asked on #234 whether Declaire's `edge` is derived and
  witnessed), #222 and #234 on their triggers.
