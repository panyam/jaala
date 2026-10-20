#!/usr/bin/env bash
# Starts or removes the shared graph-server pair in stores/bolt/compose.yml (#435).
#
#   scripts/bolt-compose.sh up     start both and wait until each answers a query
#   scripts/bolt-compose.sh down   remove both, and with them every namespace's graph
#
# Once it's up, scripts/bolt-servers.sh (the exercise, selfcheck) uses it; for go test in
# stores/bolt, export the two URLs up prints.
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose -f stores/bolt/compose.yml)
case "${1:-}" in
up)
	"${compose[@]}" up -d --wait
	echo "export JAALA_BOLT_NEO4J=bolt://127.0.0.1:17687"
	echo "export JAALA_BOLT_MEMGRAPH=bolt://127.0.0.1:17688"
	;;
down)
	"${compose[@]}" down
	;;
*)
	echo "usage: $0 up|down" >&2
	exit 2
	;;
esac
