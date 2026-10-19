#!/usr/bin/env bash
# The exercise of mission_graph_server_stores (#420): the guide's packages and a code graph kept in
# each graph server STORES names (scripts/bolt-servers.sh starts them), asked their questions over
# the server and over memory, then a session edited and committed. Each row should say same, and a
# bound question should ship far fewer tuples than the graph holds.
set -uo pipefail
cd "$(dirname "$0")/.."
./scripts/bolt-servers.sh sh -c 'cd stores/bolt && go test -count=1 -run "^TestExercise$" -v . | grep -vE "^(=== RUN|PASS$)"'
status=$?
[ $status -eq 3 ] && echo "exercise: could not run (no graph server)"
exit $status
