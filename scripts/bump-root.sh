#!/usr/bin/env bash
# Points every sub-module that requires jaala at version V and drops its replace, then tidies it, so
# the sub-module builds the way a consumer will (after mcpkit's scripts/bump-root.sh). V must be
# published: tidy fetches it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${V:-}"
MODS="${SUB_MODS_TO_TAG:-}"

if [ -z "$V" ]; then
    echo "Usage: make bump-root V=v0.1.N"
    exit 1
fi
for mod in $MODS; do
    grep -q 'github.com/panyam/jaala v' "$mod/go.mod" || continue
    echo "==> $mod/go.mod: require github.com/panyam/jaala $V"
    (cd "$mod" && go mod edit -require="github.com/panyam/jaala@$V" -dropreplace=github.com/panyam/jaala && go mod tidy)
done
