#!/usr/bin/env bash
# Tags jaala and every published sub-module at V, at HEAD (after mcpkit's scripts/tag-all.sh). Local
# only: it prints the push, which `make tag-push` runs. The root tag's annotation is the release
# notes, read from NOTES. A sub-module requiring jaala, or another of the tagged modules, at anything
# but V isn't tagged: a consumer reads the requires and ignores the replaces, so it would build
# against that older version. `make bump-root V=...` points them at V; commit that first. When V is
# tagged already, only the sub-modules are tagged, at HEAD, for a sub-module whose tag failed.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${V:-}"
NOTES="${NOTES:-}"
MODS="${SUB_MODS_TO_TAG:-}"

if [ -z "$V" ]; then
    echo "Usage: make tag V=v0.1.N NOTES=notes.txt"
    exit 1
fi
stale=0
for mod in $MODS; do
    for path in github.com/panyam/jaala $(printf 'github.com/panyam/jaala/%s ' $MODS); do
        have="$(cd "$mod" && go mod edit -json | awk -v p="\"$path\"," '
            $1 == "\"Path\":" && $2 == p { getline; gsub(/[",]/, "", $2); print $2; exit }')"
        if [ -n "$have" ] && [ "$have" != "$V" ]; then
            echo "$mod/go.mod requires $path $have, not $V"
            stale=1
        fi
    done
done
if [ "$stale" = 1 ]; then
    echo "run make bump-root V=$V and commit first"
    exit 1
fi

if git rev-parse -q --verify "refs/tags/$V" >/dev/null; then
    echo "$V is tagged already; tagging only the sub-modules at HEAD..."
else
    echo "Tagging $V across all modules..."
    if [ -n "$NOTES" ]; then
        git tag -a "$V" -F "$NOTES"
    else
        echo "  (no NOTES: $V's annotation, and so its release notes, is just its name)"
        git tag -a "$V" -m "$V"
    fi
fi

refs=""
for mod in $MODS; do
    echo "  $mod/$V"
    git tag -a "$mod/$V" -m "$mod/$V"
    refs="$refs $mod/$V"
done

echo ""
echo "Tags created locally. Push with:"
echo "  git push origin $V"
if [ -n "$refs" ]; then
    echo "  git push origin$refs"
fi
