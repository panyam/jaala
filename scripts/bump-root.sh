#!/usr/bin/env bash
# Points every sub-module at version V, the one `make tag-push` is about to tag, before tagging (after
# mcpkit's scripts/bump-root.sh and bump-pins.sh): its require of jaala, and of every other module in
# SUB_MODS_TO_TAG it requires (cypher and nlmodels require frontends/nl, mcp requires cypher, and nl
# through it), directly or not. Each such require gets a replace by the checkout, so tidy resolves
# them locally and never needs V to be published; a consumer ignores replace and reads the requires,
# which name V once the tags are pushed. All of them are tagged at one commit in one `make tag-push`.
# The docsite, untagged, is pointed at V the same way, since tidy never lowers a require.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${V:-}"
MODS="${SUB_MODS_TO_TAG:-}"

if [ -z "$V" ]; then
    echo "Usage: make bump-root V=v0.1.N"
    exit 1
fi
# The path from $1 back to the repository root: ../ per directory.
up() { echo "$1" | sed 's|[^/][^/]*|..|g'; }

for mod in $MODS docsite; do
    root="$(up "$mod")"
    edits=()
    if grep -q 'github.com/panyam/jaala v' "$mod/go.mod"; then
        edits+=(-require="github.com/panyam/jaala@$V" -replace="github.com/panyam/jaala=$root/")
    fi
    for sib in $MODS; do
        [ "$sib" = "$mod" ] && continue
        grep -q "github.com/panyam/jaala/$sib v" "$mod/go.mod" || continue
        edits+=(-require="github.com/panyam/jaala/$sib@$V" -replace="github.com/panyam/jaala/$sib=$root/$sib")
    done
    [ ${#edits[@]} -gt 0 ] || continue
    echo "==> $mod/go.mod: require jaala's modules at $V"
    (cd "$mod" && go mod edit "${edits[@]}")
done
for mod in $MODS docsite; do
    (cd "$mod" && go mod tidy)
done
