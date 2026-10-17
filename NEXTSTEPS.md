# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. `main` is 90e2f7d: PR #342 (#340, #341) merged after v0.1.66 and is untagged;
  selfcheck passed on its tree (3b9c20b). The owner picks the next version, released with
  `make tag-push V=v0.1.N NOTES=notes.txt`.
- Mission #322 (`mission_agent_frontend`) is active, 11 of 12 closed; the open one is declaire#290.
  Declaire's translator-alone arm ran on v0.1.66 (declaire#295): 2/14 on a 7B against
  declaire#288's 10. Nothing ready in jaala: the arm reruns from a Declaire session once a tag holds
  #342. Exercise logged at 90e2f7d (`TestEval` 7/8, 15 tries, fixes 5/5), no PR since.
- Off-mission and ready if picked: #143 (P3, CI's `datalog` race tests at 277s, its trigger fired).
- Waiting: #327 until a host times `Across` over a store; #288 (sessions) after #287 and agni#1027.
- This run: retriaged (filed #340, #341; closed epic #213; #143 to P3; retriggered #289; #327 under
  #183), merged #342; dropped the Across threads notes, which CLAUDE.md already holds.

## Across threads

- Nothing repo-wide beyond CLAUDE.md.
