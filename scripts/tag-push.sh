#!/usr/bin/env bash
# Pushes the tags `make tag` made at V and publishes V's release page from its annotation. jaala's
# tag goes by itself, ahead of the sub-modules': GitHub raises no push event for a push of several
# tags at once, so workflows keyed on a tag never ran (mcpkit #1391).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${V:-}"
MODS="${SUB_MODS_TO_TAG:-}"

if [ -z "$V" ]; then
    echo "Usage: make tag-push V=v0.1.N NOTES=notes.txt"
    exit 1
fi
git push origin "$V"
refs=()
for mod in $MODS; do
    refs+=("$mod/$V")
done
if [ ${#refs[@]} -gt 0 ]; then
    git push origin "${refs[@]}"
fi
gh release create "$V" --title "$V" --notes-from-tag --latest
