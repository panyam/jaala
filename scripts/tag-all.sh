#!/usr/bin/env bash
# Tags jaala and every published sub-module at V, at HEAD (after mcpkit's scripts/tag-all.sh). Local
# only: it prints the push, which `make tag-push` runs. The root tag's annotation is the release
# notes, read from NOTES. A sub-module still replacing jaala with its checkout isn't tagged: a
# consumer ignores replace, so it would build against whatever jaala its require names. When V is
# tagged already, only the sub-modules are tagged, at HEAD: a sub-module requiring another is tagged
# after make bump-root points it at that one's V, so one release's tiers share V.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${V:-}"
NOTES="${NOTES:-}"
MODS="${SUB_MODS_TO_TAG:-}"

if [ -z "$V" ]; then
    echo "Usage: make tag V=v0.1.N NOTES=notes.txt"
    exit 1
fi
for mod in $MODS; do
    if grep -q '^replace github.com/panyam/jaala' "$mod/go.mod"; then
        echo "$mod/go.mod still replaces jaala or a module of it; run make bump-root V=<a published jaala tag> and commit first"
        exit 1
    fi
done

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
