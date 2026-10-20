#!/usr/bin/env bash
# Runs one scanner over every module jaala ships (#459, after agni's): the root and MODULES, which
# the Makefile sets to SUB_MODS_TO_TAG. Each tool runs through `go run` at the version the Makefile
# pins (STATICCHECK, GOVULNCHECK, GOSEC, GITLEAKS), so CI and a laptop run the same one with nothing
# installed.
#
#   scripts/scan.sh lint|vulncheck|seccheck|secrets
#
# `./...` stays inside one module, so the root's scan alone never reaches a sub-module. Every module
# runs even after one fails, so one finding doesn't hide the rest, and the last line names them all.
# gosec runs under the Go each module's own go.mod names, as CI does: under a newer Go than its
# analysis library reads, its SSA rules (G115 among them) skip without a word, so a newer toolchain
# reads clean where CI doesn't (agni found 100 that way). Under an older Go than the module needs it
# loads nothing and still reports 0 issues (frontends/mcp's 1.26.9 under the root's 1.26.4 did), so a
# `go list` under the same toolchain runs first and fails loudly instead. A finding is fixed, or annotated on its line as
# `// #nosec G<n> -- <why>`. govulncheck is reachability-based: it passes while an advisory sits in a
# required module whose vulnerable code nothing calls. gitleaks reads the whole history, once, from
# the root, allowlisting only exact values in .gitleaks.toml.
set -uo pipefail
cd "$(dirname "$0")/.."

tool=${1:?usage: scripts/scan.sh lint|vulncheck|seccheck|secrets}
modules=". ${MODULES:-}"
case $tool in
lint) run() { go run "${STATICCHECK:?}" ./...; } ;;
vulncheck) run() { go run "${GOVULNCHECK:?}" ./...; } ;;
seccheck) run() {
	local gover
	gover=$(awk '/^go /{print $2; exit}' go.mod)
	GOTOOLCHAIN=go$gover go list ./... >/dev/null && GOTOOLCHAIN=go$gover go run "${GOSEC:?}" -quiet -severity=medium ./...
} ;;
secrets) exec go run "${GITLEAKS:?}" detect --source . --config .gitleaks.toml --redact ;;
*) echo "scan.sh: unknown scanner $tool" >&2; exit 2 ;;
esac

failed=""
for m in $modules; do
	echo "==> $tool $m"
	(cd "$m" && run) || failed="$failed $m"
done
if [ -n "$failed" ]; then
	echo "$tool failed in:$failed"
	exit 1
fi
echo "$tool clean in every shipped module"
