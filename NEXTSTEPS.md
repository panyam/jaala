# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. v0.1.57 (a88d6b7, PR #298: #293, #244, #297, and pushed groups named by their
  comparisons) is tagged with its upgrade note on #293; v0.1.56 (#291, #295) before it.
- Active: #235 (store pushdown) at 18/18 jaala tickets, nothing ready in jaala. Exercise logged at
  a88d6b7: `BenchmarkStores` unchanged; Declaire's x04 on stores 3.6s (was 2m44s at v0.1.54), all 14
  external questions pass on both engines. It stays open for Declaire's half: `ns.FilterSource` for
  `str.glob` (x05, noted on #297) and `LookupBatch` on its source (declaire#255).
- #219 (7/7) waits on Declaire's `bench -engine stores -time` on the partitioned kubernetes view; its
  exercise was last logged 2026-10-14, 14 PRs ago, but its remaining half is Declaire's.
- Off-mission P3s for the owner: #167 (tokenizer), #179, #205, #220. #278's children stay `waiting`
  (#280 blocked by #279); an agent-queries mission waits on Declaire's `make exercise-mcp-agent`
  rerun.
- This run: dropped feat/push-stops-at-filter-291 (PR #294), fix/operand-wait-deadlock (PR #296)
  and feat/generator-ranking-and-filter-push-293 (PR #298), all merged; their remote branches are
  still on origin.

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #260 if
  `bindArg`/`binding.clone` dominate (jaala's own workloads say they do, 63% of allocation, posted
  on #260 and #234), #222 and #234 on their triggers.
