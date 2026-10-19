#!/usr/bin/env bash
# Runs ./selfcheck.sh on a commit in a worktree of its own, in the foreground, and records the result
# on that commit as a git note (refs/notes/selfcheck), so `make selfcheck-status` can say when the
# last pass was and a bisect knows where to start. The checkout this runs from is never touched.
#
#   REF     the commit to check (default origin/main, fetched first)
#   SEEDS   the corpus's and Soufflé's seeds (selfcheck's JAALA_GEN_SEEDS, default 5000)
#   FUZZ    the query text's fuzzing time per target (JAALA_FUZZ_TIME, default 30s)
#   CYPHER_FUZZ  the Cypher compiler's (JAALA_CYPHER_FUZZ_TIME, default 2m)
#   NOPUSH  set to keep the note local instead of pushing refs/notes/selfcheck to origin
#
# The log goes to ~/.cache/jaala/selfcheck/<sha>-<time>.log as well as the terminal. A run that
# fails keeps its worktree, since a fuzz finding is saved inside it (testdata/fuzz/...), and says
# where it is.
set -uo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"
ref=${REF:-origin/main}
git fetch -q origin
sha=$(git rev-parse --verify -q "$ref^{commit}") || { echo "selfcheck: no commit $ref" >&2; exit 2; }
short=$(git rev-parse --short "$sha")

dir=${XDG_CACHE_HOME:-$HOME/.cache}/jaala/selfcheck
mkdir -p "$dir"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
log=$dir/$short-$stamp.log
wt=$dir/wt-$short-$stamp

git worktree add -q --detach "$wt" "$sha" || exit 2
echo "== selfcheck of $ref ($short, $(git log -1 --format=%s "$sha"))"
echo "== log $log"
echo "== worktree $wt"

start=$(date +%s)
(
	cd "$wt" &&
		env ${SEEDS:+JAALA_GEN_SEEDS="$SEEDS"} ${FUZZ:+JAALA_FUZZ_TIME="$FUZZ"} \
			${CYPHER_FUZZ:+JAALA_CYPHER_FUZZ_TIME="$CYPHER_FUZZ"} ./selfcheck.sh
) 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
secs=$(($(date +%s) - start))

result=$(grep '^selfcheck: ' "$log" | tail -n 1)
[ -n "$result" ] || result="selfcheck: did not finish (exit $status)"
note=$(
	printf '%s\n' "$result"
	printf 'ran: %s, %dm%02ds on %s, %s\n' "$stamp" $((secs / 60)) $((secs % 60)) "$(uname -sm)" "$(go version | awk '{print $3}')"
	printf 'seeds: %s, fuzz: %s, cypher fuzz: %s\n' "${SEEDS:-5000}" "${FUZZ:-30s}" "${CYPHER_FUZZ:-2m}"
	printf 'log: %s\n' "$log"
	grep -E '^== |could not run|^(--- FAIL|FAIL)' "$log" | grep -v '^== waiting'
)
# Notes from other machines first, so appending doesn't overwrite theirs on push.
git fetch -q origin '+refs/notes/selfcheck:refs/notes/selfcheck' 2>/dev/null
git notes --ref=selfcheck append -m "$note" "$sha"
if [ -z "${NOPUSH:-}" ]; then
	git push -q origin refs/notes/selfcheck || echo "== the note wasn't pushed; git push origin refs/notes/selfcheck sends it"
fi

if [ "$status" -eq 0 ]; then
	git worktree remove --force "$wt"
else
	echo "== kept the worktree, which holds any fuzz input a run saved: $wt (git worktree remove --force it after)"
fi
echo "== recorded on $short: $result"
exit "$status"
