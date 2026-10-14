# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches; everything is merged and released (v0.1.48 on 8c0c8f7, 2026-10-14).
- #235 `mission_store_pushdown` is 4/8 (#236 to #239 closed). Next ready: #240 (push aggregates to
  each store and combine them exactly; `count-lines` still ships every match), then #241 (pages),
  #246 (P2) and #250 (P3). Exercise logged at 8c0c8f7, after PR #251; nothing merged since.
- #219 `mission_store_federation` has all 7 jaala tickets closed and waits on Declaire's half:
  `bench -engine stores -time` on a federated kubernetes view against declaire#180's baseline,
  from a Declaire session. Declaire took v0.1.47 on 2026-10-14 (declaire#224, closing declaire#211)
  and runs #177's exercises as `bench -engines` (declaire#222), but hasn't quoted the federated run
  on #219 yet. Logged at 5cb5ba2.
- No P0 or P1 is open. Off-mission and ready: #179, #205, #206, #209, #215, #220, #222, #234 (P3);
  #207, #242 and #244 `waiting`.
- This run: merged PR #251 (#239) and released v0.1.48; filed #250.

## Across threads

- Soufflé isn't installed on the Mac this repo was last worked from, so `./selfcheck.sh` skips its
  Soufflé step there; CI's `souffle` job is the check to quote.
- Declaire's measurement decides the next #219-adjacent ticket: #242 if lookup buckets are huge,
  #234 if strings dominate the heap (declaire#209), #222 if two indexed lookups are mis-ranked.
