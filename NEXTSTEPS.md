# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. v0.1.59 (5b381b6, PR #301: #205, prefixed Source values folded, SI units) is
  tagged; it breaks agni (board geometry in mm now folds to m), and the upgrade note is agni#1035.
  v0.1.58 (6c77e12: #220 derived store, #260 bindings in place) before it; #220's usage is on
  declaire#205.
- Active: #235 (store pushdown, 18/18) and #219 (7/7), nothing ready in jaala. Exercise logged on
  both at 5b381b6: `BenchmarkStores` unchanged from a88d6b7. Both wait on Declaire: `ns.FilterSource`
  for `str.glob` (x05), `LookupBatch` on its source (declaire#255), and `bench -engine stores -time`
  on the partitioned kubernetes view.
- Off-mission P3s for the owner: #167 (tokenizer), #179 (timed facts, unblocks 2). #302 (`waiting`)
  holds #205's leftovers. #278's children stay `waiting` (#280 blocked by #279); an agent-queries
  mission waits on Declaire's `make exercise-mcp-agent` rerun.
- This run: no threads to drop; PRs #299, #300 and #301 merged, their branches gone from origin.

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #222 and
  #234 on their triggers. #260 is done, so the next allocators in jaala's own workloads
  (`addTuple`, `since`, `idbIndex.sync`) are #234's to weigh.
