# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. #292 (#290, generator-only relations read in full, one relation's two
  adornments no longer dropping demand everywhere) merged as e2f7bb2 and is tagged v0.1.55, upgrade
  note on #290. Declaire's x04 on stores: 2m44s to 51.5s (mem 4.7s).
- Active: #235 (store pushdown) at 15/16, next ready #291 (P1): a pushed group grows past the
  `sends` filter (25s of x04's 51.5s) and a demand guard from an all-free generator lists all 136,181
  functions. Exercise logged at e2f7bb2 (`BenchmarkStores` unchanged; the change shows on x04).
- #219 (7/7) waits on Declaire's `bench -engine stores -time` on the partitioned kubernetes view.
  declaire#255 holds Declaire's half (per-store fan-out per walk, 47ms per Partial push).
- Off-mission P3s for the owner: #167 (tokenizer), #179, #205, #220. #278's children stay `waiting`
  (#280 blocked by #279); an agent-queries mission waits on Declaire's `make exercise-mcp-agent`
  rerun on v0.1.54.
- This run: dropped feat/constant-run-demand-290 (PR #292 merged).

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #260 if
  `bindArg`/`binding.clone` dominate (jaala's own workloads say they do, 63% of allocation, posted
  on #260 and #234), #222 and #234 on their triggers.
