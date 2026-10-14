# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches; everything is merged and released (v0.1.47 on 5cb5ba2, 2026-10-14).
- #235 `mission_store_pushdown` is 3/7 (#236, #237, #238 closed). Next ready: #239 (a recursive
  relation over one store's facts as one call; `deep-walk` is still 60 calls), then #240, #241 and
  #246, all P2. Exercise last logged at f37485b, after PR #249; PR #248 (#216, arithmetic only)
  merged since.
- #219 `mission_store_federation` has all 7 jaala tickets closed and waits on Declaire's half:
  `bench -engine stores -time` on a federated kubernetes view against declaire#180's baseline.
  Declaire pins jaala v0.1.44, so the run starts with a bump to v0.1.47, from a Declaire session.
  Logged at 5cb5ba2.
- No P0 or P1 is open. Off-mission and ready: #179, #205, #206, #209, #215, #220, #222, #234 (P3);
  #207, #242 and #244 `waiting`.
- This run: merged PR #247 (#237), PR #249 (#238) and PR #248 (#216, an outside contributor's);
  released v0.1.45, v0.1.46 and v0.1.47; filed #246; dropped the old resume list (all done).

## Across threads

- Soufflé isn't installed on the Mac this repo was last worked from, so `./selfcheck.sh` skips its
  Soufflé step there; CI's `souffle` job is the check to quote.
- Declaire's measurement decides the next #219-adjacent ticket: #242 if lookup buckets are huge,
  #234 if strings dominate the heap (declaire#209), #222 if two indexed lookups are mis-ranked.
