# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. v0.1.60 (24725c7: #304 generator ranked by its modes' bound arguments, #303 a
  Source's units read as tags unless `FoldSourceUnits`, any exact unit scale) is tagged; selfcheck
  passed on it. agni#1037 points at it; agni (metres since agni#1036) and Declaire change nothing.
- No active mission: #235 and #219 closed at v0.1.60 (exercise unchanged; declaire#255 closed and
  Declaire no longer plans `FilterSource` or a bound `loc.home`). Next: `retriage` to start one;
  the lead is #179 (timed facts), which declaire#268's in-memory diff and agni#971 both need.
- Off-mission P3s for the owner: #167 (tokenizer), #179 (timed facts, unblocks 2). `waiting`: #302
  (#205's leftovers), #278's children, and #304's second point (an empty materialized derived
  relation ranks after an index, unfiled, per #306's out of scope) if a host hits it.
- This run: PRs #299, #300, #301, #305 (this session) and #306 (a parallel session) merged; v0.1.58
  to v0.1.60 tagged.

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #222 and
  #234 on their triggers. #260 is done, so the next allocators in jaala's own workloads
  (`addTuple`, `since`, `idbIndex.sync`) are #234's to weigh.
