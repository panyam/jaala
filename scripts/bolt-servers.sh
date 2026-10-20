#!/usr/bin/env bash
# Runs a command against the graph servers stores/bolt's tests read (#411), each named by a bolt://
# URL in JAALA_BOLT_NEO4J or JAALA_BOLT_MEMGRAPH. STORES picks which (default neo4j,memgraph). A
# server already running, natively or in Docker, is used as its URL says; with no URL, the shared
# pair make servers-up starts (#435) is used when it's up, and otherwise the server is started in a
# Docker container on a free port and removed after (KEEP=1 keeps it). Each run's tests keep to a
# namespace of their own, so runs from several worktrees share the pair safely. A server not chosen
# is unset, so the tests skip it. EXCLUSIVE=1 skips the shared pair and starts a throwaway server,
# and sets JAALA_BOLT_EXCLUSIVE for the tests that clear a whole server (#413's Cypher differential).
#
#   scripts/bolt-servers.sh <command...>
#
# It exits 3 without running the command when no server chosen could be used ("could not run"),
# and otherwise with the command's status. The command runs with JAALA_BOLT_REQUIRE=1, so a server
# that went missing fails the tests rather than skipping them.
set -uo pipefail

stores=${STORES:-neo4j,memgraph}
started=()
cleanup() {
	[ -n "${KEEP:-}" ] && return
	for c in ${started[@]+"${started[@]}"}; do docker rm -f "$c" >/dev/null 2>&1; done
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

for s in ${stores//,/ }; do
	case $s in
	neo4j | memgraph) ;;
	*) echo "bolt-servers: no server named $s (STORES takes neo4j and memgraph)" >&2; exit 2 ;;
	esac
done

chosen=0 missing=()
for s in neo4j memgraph; do
	case $s in
	neo4j) var=JAALA_BOLT_NEO4J image=neo4j:5.26 ready='Started.' flags=(-e NEO4J_AUTH=none) ;;
	memgraph) var=JAALA_BOLT_MEMGRAPH image=memgraph/memgraph:3.6.0 ready='You are running Memgraph' flags=() ;;
	esac
	case ",$stores," in
	*",$s,"*) ;;
	*) unset "$var"; continue ;;
	esac
	if [ -n "${!var:-}" ]; then
		echo "== $s at ${!var}"
		chosen=$((chosen + 1))
		continue
	fi
	if ! docker info >/dev/null 2>&1; then
		missing+=("$s: no $var and no Docker to start one")
		continue
	fi
	if [ -z "${EXCLUSIVE:-}" ] && shared=$(docker compose -f "$(dirname "$0")/../stores/bolt/compose.yml" port "$s" 7687 2>/dev/null) && [ -n "$shared" ]; then
		export "$var=bolt://$shared"
		echo "== $s from the shared pair at ${!var}"
		chosen=$((chosen + 1))
		continue
	fi
	name=jaala-bolt-$s-$$
	if ! docker run -d --name "$name" -p 127.0.0.1::7687 ${flags[@]+"${flags[@]}"} "$image" >/dev/null; then
		missing+=("$s: docker run $image failed")
		continue
	fi
	started+=("$name")
	up=no
	for _ in $(seq 1 120); do
		if docker logs "$name" 2>&1 | grep -q "$ready"; then up=yes; break; fi
		sleep 1
	done
	if [ $up = no ]; then
		missing+=("$s: $image didn't come up in two minutes")
		continue
	fi
	export "$var=bolt://$(docker port "$name" 7687/tcp | head -n 1)"
	echo "== $s started in $name at ${!var}"
	chosen=$((chosen + 1))
done
for m in ${missing[@]+"${missing[@]}"}; do echo "  could not run: $m"; done
if [ $chosen -eq 0 ]; then
	exit 3
fi
[ -n "${EXCLUSIVE:-}" ] && export JAALA_BOLT_EXCLUSIVE=1
JAALA_BOLT_REQUIRE=1 "$@"
