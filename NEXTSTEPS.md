# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches; everything is merged and released (v0.1.50 on ee2a635, 2026-10-14).
- Both active missions have every jaala ticket closed and wait on Declaire's run, from a Declaire
  session: bump from v0.1.47 to v0.1.50, build the view's federation with
  `NewPartitionedFederation`, give the SQLite fragment stores the capabilities they can support
  (`PushSource`, `SummarySource`, `WalkSource`, `ConjunctionWalkSource`), and run
  `bench -engine stores -time` on the kubernetes view against declaire#180's baseline. Quoting it on
  #219 and #235 closes both, or names the next ticket.
- #235 `mission_store_pushdown`: 11/11, exercise logged at ee2a635. #219
  `mission_store_federation`: 7/7, logged at 5cb5ba2 (nothing since changes its columns).
- No P0, P1 or P2 is open. P3, off-mission: #179, #205, #206, #209, #215, #220, #222, #234.
  `waiting`: #207, #242, #244, #259.
- This run: merged #255 to #258 and #261, #262 (closing #240, #241, #246, #250, #252 to #254);
  released v0.1.49 and v0.1.50; filed #259 (`waiting`).

## Across threads

- Soufflé isn't installed on the Mac this repo was last worked from, so `./selfcheck.sh` skips its
  Soufflé step there; CI's `souffle` job is the check to quote.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #234 if
  strings dominate the heap (declaire#209), #222 if two indexed lookups are mis-ranked, #259 if a
  join over its partitioned stores shows lookups per literal per round.
