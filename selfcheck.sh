#!/bin/sh
# selfcheck runs jaala's checks that need no host: every evaluator against Naive on generated
# programs (#87) and the grammar against them (#323), work against baselines (#88), fuzzed query text
# (#89), answers against Soufflé (#23), when souffle is on PATH, and llama.cpp decoding under nl's
# grammar (#330), when `make llama` has fetched it, and stores/bolt over Neo4j and Memgraph (#411),
# when a server is running or Docker can start one (STORES picks which). See #86. JAALA_GEN_SEEDS overrides the seed count,
# for the corpus and for Soufflé, JAALA_FUZZ_TIME the query text's fuzzing time per target, and
# JAALA_CYPHER_FUZZ_TIME the Cypher compiler's, and JAALA_BOLT_SEEDS the corpus's seeds over a graph
# server (1000).
set -u
cd "$(dirname "$0")"

# One selfcheck at a time on the machine, whichever checkout or session starts it: two at once each
# run past the corpus's timeout under the load. The lock is a directory holding its run's pid, and a
# run that died leaves one whose pid is gone, which the next run takes over. A shell loop around
# pgrep did this before and waited forever: its own command line matched the pattern.
lock=${JAALA_SELFCHECK_LOCK:-${XDG_CACHE_HOME:-$HOME/.cache}/jaala/selfcheck.lock}
mkdir -p "$(dirname "$lock")"
waiting=no
while ! mkdir "$lock" 2>/dev/null; do
	holder=$(cat "$lock/pid" 2>/dev/null)
	# A lock with no pid a minute after it was made belongs to a run that died before writing one.
	if { [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; } ||
		{ [ -z "$holder" ] && [ -n "$(find "$lock" -maxdepth 0 -mmin +1 2>/dev/null)" ]; }; then
		rm -rf "$lock"
		continue
	fi
	[ $waiting = no ] && echo "== waiting for the selfcheck running as pid ${holder:-?} ($lock)"
	waiting=yes
	sleep 20
done
echo $$ >"$lock/pid"

seeds=${JAALA_GEN_SEEDS:-5000}
fuzztime=${JAALA_FUZZ_TIME:-30s}
# The Cypher compiler gets longer: #352's second PR took five 10-minute runs to come back clean, four
# of them finding a bug, and 30 seconds found none of the four.
cypherfuzztime=${JAALA_CYPHER_FUZZ_TIME:-2m}
out=$(mktemp)
llama_pid=
cleanup() {
	[ -n "$llama_pid" ] && kill "$llama_pid" 2>/dev/null
	rm -f "$out" "$out.llama"
	rm -rf "$lock"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
status=0

echo "== generated programs: $seeds seeds"
JAALA_GEN_SEEDS=$seeds go test -count=1 -timeout 30m ./datalog -run '^(TestGeneratedProgramsAgree|TestBrokenProgramsFailTheSameWay|TestTheGrammarAcceptsEveryProgramAsJaalaSpellsIt)$' -v >"$out" 2>&1 || status=1
# Relation names a rewrite adds carry a NUL, which would make grep treat the log as binary.
tr -d '\000' <"$out" | grep -E 'seeds$|compared|skipped|known|broken programs|each accepted|written:|disagrees|refuses|replay|shrunk|facts:|bind:|naive:|planned:|seminaive:|^(--- |ok|FAIL)'

echo "== work against baselines"
go test -count=1 ./datalog -run '^TestWorkStaysWithinBaseline$' -v >"$out" 2>&1 || status=1
grep -E 'work |over its|under its|has no baseline|no workload|^(--- |ok|FAIL)' "$out" | sed 's/^ *bench_test.go:[0-9]*: /  /'

echo "== fuzzed query text: $fuzztime per target, the Cypher compiler $cypherfuzztime"
# pkg is the directory of the package holding the target: datalog's query text, or the Cypher
# compiler's, which is a module of its own, so go test runs from inside it.
fuzz() { (cd "$pkg" && go test -count=1 . -run '^$' -fuzz "^$1\$" -fuzztime "$ft") >"$out" 2>&1; }
# saved is the input a failed run wrote, if any; it is a finding only if it still fails replayed.
saved() { tr -d '\000' <"$out" | sed -n 's|.*Failing input written to testdata/fuzz/[^/]*/\([0-9a-f]*\).*|\1|p' | head -n 1; }
replays() { (cd "$pkg" && go test -count=1 . -run "^$1/$2\$") >/dev/null 2>&1; }
tail30() { tr -d '\000' <"$out" | tail -n 30 | sed 's/^/    /'; }
# fuzzed runs a target and reports it: 0 when it found nothing, 1 for an input that fails replayed,
# 2 when the fuzz engine failed instead (#169): a worker killed or hung, out of memory, or the
# machine under load. A killed worker still saves the input it was running, which then passes
# replayed, so that file is removed rather than left to be committed as a seed.
fuzzed() {
	fuzz "$1" && return 0
	id=$(saved)
	if [ -n "$id" ] && ! replays "$1" "$id"; then
		tr -d '\000' <"$out" | grep -E 'Failing input|^\s+fuzz_test.go|panic:|FAIL' | sed "s/^/  $1: /"
		return 1
	fi
	if [ -n "$id" ]; then
		rm -f "$pkg/testdata/fuzz/$1/$id"
		echo "  $1: the fuzz engine failed; the input it saved ($id) passes replayed, so it was removed. Its last lines:"
	else
		echo "  $1: the fuzz engine failed without saving an input. Its last lines:"
	fi
	tail30
	return 2
}
for pair in datalog:FuzzParse datalog:FuzzEval frontends/cypher:FuzzCompile; do
	pkg=${pair%%:*} target=${pair#*:} ft=$fuzztime
	[ "$pkg" = frontends/cypher ] && ft=$cypherfuzztime
	# go test fuzzes one target at a time. A failing input is written to <pkg>/testdata/fuzz/<target>,
	# which plain go test then replays: fix it, and commit the file as a seed. An engine failure gets
	# one rerun, so it reads differently from a finding.
	fuzzed "$target"
	case $? in
	0) echo "  $target: no failing input in $ft" ;;
	1) status=1 ;;
	2) if fuzzed "$target"; then
		echo "  $target: no failing input in $ft on a rerun, so the first run's failure was the engine's"
	else
		status=1
	fi ;;
	esac
done

echo "== Soufflé differential: $seeds seeds"
souffle_ran=yes
if command -v souffle >/dev/null 2>&1; then
	# The corpus's programs Soufflé can express, translated and run there, against Naive's answers.
	JAALA_SOUFFLE=require JAALA_SOUFFLE_SEEDS=$seeds go test -count=1 -timeout 30m ./datalog -run '^(TestSouffleAgreesWithNaive|TestSouffleCatchesAWrongTranslation)$' -v >"$out" 2>&1 || status=1
	tr -d '\000' <"$out" | grep -E 'seeds$|compared with|skipped:|disagreements|disagrees|refused|replay|shrunk|facts:|bind:|naive:|souffle:|^(--- |ok|FAIL)'
else
	souffle_ran=no
	echo "  could not run: souffle is not on PATH (make setup installs it)"
fi

boltseeds=${JAALA_BOLT_SEEDS:-1000}
echo "== graph servers: stores/bolt over ${STORES:-neo4j,memgraph}, $boltseeds seeds"
bolt_ran=yes
# The corpus, a session's commit and the exercise over each server scripts/bolt-servers.sh can use
# or start (#411): one already running by its JAALA_BOLT_* URL, or a Docker container.
JAALA_BOLT_SEEDS=$boltseeds ./scripts/bolt-servers.sh sh -c 'cd stores/bolt && go test -count=1 -timeout 30m . -v' >"$out" 2>&1
case $? in
0) ;;
3) bolt_ran=no ;;
*) status=1 ;;
esac
tr -d '\000' <"$out" | grep -E '^== |could not run|seeds:|seeds,|seed [0-9]+|DIFFERS|^(--- |ok|FAIL)'

echo "== llama.cpp keeps a model to nl's grammar"
llama_ran=yes
model=${JAALA_LLAMA_MODEL:-${XDG_CACHE_HOME:-$HOME/.cache}/jaala/stories260K.gguf}
if command -v llama-server >/dev/null 2>&1 && [ -f "$model" ]; then
	# A real decoder reading the grammar nl renders (#330), on llama.cpp's 1 MB test model.
	port=$((18000 + $$ % 1000))
	llama-server -m "$model" --host 127.0.0.1 --port "$port" --chat-template chatml >"$out.llama" 2>&1 &
	llama_pid=$!
	up=no
	for _ in $(seq 1 60); do
		if curl -fs "http://127.0.0.1:$port/health" >/dev/null 2>&1; then up=yes; break; fi
		sleep 0.5
	done
	if [ $up = yes ]; then
		(cd frontends/nlmodels && JAALA_LLAMACPP_URL="http://127.0.0.1:$port" go test -count=1 ./llamacpp -run '^TestARealServerKeepsTheModelToTheGrammar$' -v) >"$out" 2>&1 || status=1
		grep -E 'finished on their own|leaves the grammar|isn.t a program|refused|control:|^(--- |ok|FAIL)' "$out"
	else
		status=1
		echo "  llama-server didn't come up:"; tail -5 "$out.llama"
	fi
	kill $llama_pid 2>/dev/null
else
	llama_ran=no
	echo "  could not run: llama-server or $model is missing (make llama fetches both)"
fi

missing=""
[ $souffle_ran = no ] && missing="Soufflé"
[ $llama_ran = no ] && missing="${missing:+$missing and }llama.cpp"
[ $bolt_ran = no ] && missing="${missing:+$missing and }the graph servers"
if [ $status -ne 0 ]; then
	echo "selfcheck: FAIL"
elif [ -n "$missing" ]; then
	echo "selfcheck: pass, except $missing, which could not run"
else
	echo "selfcheck: pass"
fi
exit $status
