#!/usr/bin/env bash
# Says where selfcheck stands on origin/main, from the notes scripts/selfcheck-run.sh leaves on each
# commit it checked: the last pass, the last failure, how many PRs have merged since the last pass,
# and whether a run is due (every EVERY merged PRs, default 5). After a failure it prints the bisect
# that starts from the last pass.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)"
every=${EVERY:-5}
git fetch -q origin main '+refs/notes/selfcheck:refs/notes/selfcheck' 2>/dev/null

pass= fail=
while read -r sha; do
	first=$(git notes --ref=selfcheck show "$sha" 2>/dev/null | grep '^selfcheck: ' | tail -n 1)
	[ -n "$first" ] || continue
	case $first in
	"selfcheck: pass"*) [ -z "$pass" ] && pass=$sha ;;
	*) [ -z "$fail" ] && fail=$sha ;;
	esac
	[ -n "$pass" ] && [ -n "$fail" ] && break
done < <(git rev-list --first-parent origin/main)

show() { printf '  %s %s\n    %s\n' "$(git rev-parse --short "$1")" "$(git log -1 --format='%ad %s' --date=short "$1")" "$(git notes --ref=selfcheck show "$1" | sed -n '1,3p' | paste -sd ';' - | sed 's/;/, /g')"; }

if [ -z "$pass" ]; then
	echo "no selfcheck has passed on origin/main yet: make selfcheck runs one"
	exit 0
fi
echo "last pass:"
show "$pass"
merged=$(git rev-list --count --merges --first-parent "$pass..origin/main")
if [ -n "$fail" ] && git merge-base --is-ancestor "$pass" "$fail"; then
	echo "failed since:"
	show "$fail"
	echo "  bisect: git bisect start $(git rev-parse --short "$fail") $(git rev-parse --short "$pass")"
fi
echo "PRs merged since the last pass: $merged"
if [ "$merged" -ge "$every" ]; then
	echo "due: make selfcheck (every $every merged PRs)"
else
	echo "not due until $every PRs have merged"
fi
