# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. #286 (#276, the clause endings agents write) merged as 163c0f4 and is tagged
  v0.1.54 with its upgrade note on #276: Declaire changes no code, bumps, and reruns `make
  exercise-mcp-agent` against 69 of 131 failed queries (declaire#246).
- Both active missions have every jaala ticket closed (#219 7/7, #235 14/14) and wait on Declaire's
  run: `bench -engine stores -time` on the partitioned kubernetes view against declaire#180's
  baseline. #235's exercise last logged at bb965ca; 7 PRs merged since, none of them pushdown work.
- Nothing ready on a mission. Off-mission: the #278 epic (Cypher/GQL gaps, children #279 to #284,
  all `waiting`) is untriaged; `retriage` should decide whether it becomes jaala's next mission.
  Left for the owner: #179, #205, #220 (P3), and #167 (tokenizer, unparked at P3 when FuzzParse
  found a fifth slicing bug).
- Retriage 2026-10-15: #278 stays an epic (no exercise until Declaire's Cypher front end refuses
  queries over its gaps); #280 is blocked by #279. An agent-queries mission (exercise: Declaire's
  `make exercise-mcp-agent`) waits on Declaire's rerun on v0.1.54; start it if jaala failures remain.
- This run: dropped feat/agent-clause-endings-276 (PR #286 merged); v0.1.54's selfcheck passed.

## Across threads

- `make setup` installs Soufflé 2.5 (Homebrew on macOS), so `./selfcheck.sh` runs its Soufflé step
  locally.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #260 if
  `bindArg`/`binding.clone` dominate (jaala's own workloads say they do, 63% of allocation, posted
  on #260 and #234), #222 and #234 on their triggers.
