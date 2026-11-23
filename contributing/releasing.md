# Releasing and PRs

Tagging a release, and working with branches, stacked PRs and other sessions.

## Releasing

Merge the PR, then start the CI set on the merged `main` (PRs that merged together were never
tested together), and `make selfcheck` when `make selfcheck-status` says one is due. The owner picks the version; so far releases are patch bumps
on v0.1.x, breaking changes included, pre-1.0, and a tag needn't wait for the checks: when they
fail, fix it and tag the next patch. agni consumes tags only (`go get github.com/panyam/jaala@vX`),
never a `replace`.

Release with `make tag-push V=v0.1.N NOTES=notes.txt` on the merge commit (`scripts/`, after
mcpkit's). It tags jaala and every module in the Makefile's `SUB_MODS_TO_TAG`
(`frontends/nl`, tagged `frontends/nl/v0.1.N`, then `nlmodels`, `mcp` and `cypher`) at the same commit, pushes jaala's tag
alone and then the others (GitHub raises no push event for several tags in one push, mcpkit #1391),
and publishes the release page from jaala's annotation, which is the release notes: a short list
of what changed, from NOTES. `make tag` alone tags without pushing.

Every sub-module keeps a `replace` of jaala, and of each tagged module it requires (`cypher` and
`nlmodels` require `frontends/nl`, `mcp` requires `cypher` and through it nl), by the checkout, so
its CI and tests build against `main`, and its requires name the last release. Before tagging, `make
bump-root V=v0.1.N` (mcpkit's model: `scripts/bump-root.sh`) points those requires, and the
docsite's, at the version about to be tagged with `go mod edit`, adds any missing replace, and
tidies through the replaces, so V needn't exist yet; commit it to `main`, then `make tag-push` on
that commit. A consumer ignores `replace` and reads the requires, which resolve once the tags are
pushed. `tag-all` refuses a sub-module requiring any of jaala's modules at a version other than V
(bump-root forgotten). With V tagged already it tags only the sub-modules, at HEAD, for a sub-module
tag that failed, and `tag-push` then pushes only theirs. After pushing, check each resolves:
`GOPROXY=direct go list -m github.com/panyam/jaala/<dir>@V` from a scratch module. v0.1.72 to
v0.1.75 went out in tiers, from a `bump-root` that dropped the replaces and tidied against
published tags; that is why they took several commits. Put the upgrade note on the issue a
host filed or the PR's own issue.

## Branches and PRs

Create the branch before the first commit (`git branch --show-current` says so). #200's work was
committed on local `main`, so `git reset --soft main` to squash it did nothing and the PR opened
with WIP commits. Squash against `origin/main`, never a local name that may have moved.

Another session may be working the same ticket: #445 was built twice in parallel (#451 merged, #452
closed as its duplicate). Before branching, `gh pr list --state all --search '<N>'` for a PR naming
the ticket, and again before opening yours.

Another session may tag a release while your PR is open: v0.1.65 shipped `nl` at the root while
#337, which moved it, was being planned on "no tag ships it yet". Check `git fetch --tags` and
`git tag --contains` before arguing that a path or API change is free.

Stacked PRs (each branched off the one below, its base that branch) review and merge in order, and
GitHub retargets the next to `main` when its base merges; confirm each merge commit is an ancestor of
`origin/main` before deleting branches. Merged first, the upper PR lands in the lower's branch, not
`main` (#275 did): its issue stays open until the lower PR merges, and is closed by hand then. A branch whose change the corpus or fuzzing should see at
full size gets `make selfcheck REF=<branch>`, which makes the detached worktree itself, so the checkout
stays free for the next branch. A selfcheck run on the working tree while editing it tests half-written
code, and so does one whose tree gets a scratch test dropped in (a before/after capture belongs in
another worktree). Run it in the background as `./selfcheck.sh > log 2>&1` with nothing after it: a trailing
`; echo exit $?` made the task report 0 for a selfcheck that failed (#209's, which found #273).
When several branches are in flight (parallel agents), run their selfchecks one at a time, and
timings only on an otherwise idle machine; never `pkill -f selfcheck.sh`, which kills every run on
the machine (#220's agent did). When two PRs branched from one base merge together (#305 and #306
did), the next due run on `main` is the first that tests them together, so don't let it slip. Another
session may be running one too: `selfcheck.sh` waits for it on its own (a lock directory under
`~/.cache/jaala`, `JAALA_SELFCHECK_LOCK`). The `pgrep` loop it replaced matched its own shell and
waited forever.

Pushing a branch can get a PR opened for it under the owner's account (#168 was), titled and
bodied from the commit. Check `gh pr list --head <branch>` before `gh pr create`, and edit that PR
rather than opening a second one. A PR from an outside contributor gets the owner's review before
it merges, however green its CI: such PRs tend to settle a ticket's open questions on their own
(#184). When `main` moves under one, merge `main` into its branch (maintainers may push to it)
rather than rebasing, so the contributor's commit stays as written, and say so on the PR with what
the conflict resolution kept. Deleting an open PR's head branch closes it unmerged (#187 was closed that way), so check
`gh pr view N --json state` says `MERGED` before cleaning up a branch.

A PR body closes only its own issue. GitHub reads any "fix", "fixes", "close", "closes" or "resolves"
before `#N` as a closing keyword, even inside a phrase like "Engine fixes: #90", which closed #90 on
merge (reopened). In "Out of scope" and "Related" write "left for #N".
The same holds across repos: a host's PR that writes "fixes panyam/jaala#N" closes the jaala ticket
when the host's PR merges. declaire#365 read "ships the translator-loop fixes panyam/jaala#514 and
#517" and closed #514 with two of its pieces open (reopened). A host session writes "for
panyam/jaala#N" or "from panyam/jaala#N", and when a jaala ticket closes without a jaala PR, check
its timeline for a cross-repo close before treating it as done.
