# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches; everything is merged and released (v0.1.50 on ee2a635, 2026-10-14). agni and
  Declaire are being bumped to v0.1.50.
- Both active missions have every jaala ticket closed and wait on Declaire's run, from a Declaire
  session: build the view's federation with `NewPartitionedFederation`, give the SQLite fragment
  stores the capabilities they can support (`PushSource`, `SummarySource`, `WalkSource`,
  `ConjunctionWalkSource`; declaire#231 is its push step), and run `bench -engine stores -time` on
  the kubernetes view against declaire#180's baseline. Quoting it on #219 and #235 closes both, or
  names the next ticket.
- #219 `mission_store_federation`: 7/7, exercise logged at e31a11f (2026-10-14). #235
  `mission_store_pushdown`: 11/11, logged at ee2a635.
- Next ready: #264 (P2, off-mission), stream a goal's whole read into its answer so a `limit`
  stops it; plan it with `/start-pr` first, since the open decision is when a streamed read
  skips `edbCache`. #215 (P3) is a small correctness fix. Other P3: #179, #205, #206, #209, #220.
- No other mission is open. Candidates for the next (`/retriage`): #220 if Declaire moves its
  linkers onto rules (declaire#205), timed facts (#183) if agni#95 or declaire#169 comes first.
- This run: logged #219's exercise; filed #263 (`waiting`) and #264 (P2); moved #222 and #234
  from P3 to `waiting` on Declaire's measurements.

## Across threads

- Soufflé isn't installed on the Mac this repo was last worked from, so `./selfcheck.sh` skips its
  Soufflé step there; CI's `souffle` job is the check to quote.
- Declaire's measurement decides what's next in jaala: #242 if lookup buckets are huge, #259 if a
  join over its partitioned stores shows lookups per literal per round, #263 if a store ships a
  whole relation for a literal it could filter, #260 if `bindArg`/`binding.clone` dominate (asked
  on #234 whether Declaire's `edge` is derived and witnessed), #222 and #234 on their triggers.
