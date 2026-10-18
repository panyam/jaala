#!/usr/bin/env bash
# Points every sub-module that requires jaala at version V and drops its replace, then tidies it, so
# the sub-module builds the way a consumer will (after mcpkit's scripts/bump-root.sh). V must be
# published: tidy fetches it. A sub-module that requires another (cypher, mcp and nlmodels require
# frontends/nl) is pointed at that one's <dir>/V too, and is left as it is, replaces and all, until
# that tag is pushed: tag the one it needs first, then run this again.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${V:-}"
MODS="${SUB_MODS_TO_TAG:-}"

if [ -z "$V" ]; then
    echo "Usage: make bump-root V=v0.1.N"
    exit 1
fi
waiting=0
for mod in $MODS; do
    grep -q 'github.com/panyam/jaala v' "$mod/go.mod" || continue
    edits=(-require="github.com/panyam/jaala@$V" -dropreplace=github.com/panyam/jaala)
    ready=yes
    for sib in $MODS; do
        [ "$sib" = "$mod" ] && continue
        grep -q "github.com/panyam/jaala/$sib v" "$mod/go.mod" || continue
        if ! git ls-remote --exit-code --tags origin "refs/tags/$sib/$V" >/dev/null 2>&1; then
            echo "==> $mod/go.mod: left as it is, since it requires $sib and $sib/$V isn't pushed"
            echo "    tag it first (make tag-push V=<next> NOTES=... SUB_MODS_TO_TAG=$sib), then bump-root to that"
            ready=no waiting=1
            break
        fi
        edits+=(-require="github.com/panyam/jaala/$sib@$V" -dropreplace="github.com/panyam/jaala/$sib")
    done
    [ "$ready" = yes ] || continue
    echo "==> $mod/go.mod: require github.com/panyam/jaala $V"
    (cd "$mod" && go mod edit "${edits[@]}" && go mod tidy)
done
# The docsite replaces jaala and every front end with the checkout rather than requiring a tag, but its
# go.mod still lists what they require, so a bump above leaves it stale and the docs workflow's go
# test stops with "updates to go.mod needed" (v0.1.72's did).
echo "==> docsite/go.mod: tidy"
(cd docsite && go mod tidy)
exit $waiting
