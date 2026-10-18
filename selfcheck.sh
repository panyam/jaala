#!/bin/sh
# selfcheck runs jaala's checks that need no host: every evaluator against Naive on generated
# programs (#87) and the grammar against them (#323), work against baselines (#88), fuzzed query text
# (#89), answers against Soufflé (#23), when souffle is on PATH, and llama.cpp decoding under nl's
# grammar (#330), when `make llama` has fetched it. See #86. JAALA_GEN_SEEDS overrides the seed count,
# for the corpus and for Soufflé, JAALA_FUZZ_TIME the query text's fuzzing time per target, and
# JAALA_CYPHER_FUZZ_TIME the Cypher compiler's.
set -u
cd "$(dirname "$0")"
seeds=${JAALA_GEN_SEEDS:-5000}
fuzztime=${JAALA_FUZZ_TIME:-30s}
# The Cypher compiler gets longer: #352's second PR took five 10-minute runs to come back clean, four
# of them finding a bug, and 30 seconds found none of the four.
cypherfuzztime=${JAALA_CYPHER_FUZZ_TIME:-2m}
out=$(mktemp)
trap 'rm -f "$out"' EXIT
status=0

echo "== generated programs: $seeds seeds"
JAALA_GEN_SEEDS=$seeds go test -count=1 ./datalog -run '^(TestGeneratedProgramsAgree|TestBrokenProgramsFailTheSameWay|TestTheGrammarAcceptsEveryProgramAsJaalaSpellsIt)$' -v >"$out" 2>&1 || status=1
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

echo "== llama.cpp keeps a model to nl's grammar"
llama_ran=yes
model=frontends/nlmodels/testdata/stories260K.gguf
if command -v llama-server >/dev/null 2>&1 && [ -f "$model" ]; then
	# A real decoder reading the grammar nl renders (#330), on llama.cpp's 1 MB test model.
	port=$((18000 + $$ % 1000))
	llama-server -m "$model" --host 127.0.0.1 --port "$port" --chat-template chatml >"$out.llama" 2>&1 &
	llama_pid=$!
	trap 'kill $llama_pid 2>/dev/null; rm -f "$out" "$out.llama"' EXIT
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
if [ $status -ne 0 ]; then
	echo "selfcheck: FAIL"
elif [ -n "$missing" ]; then
	echo "selfcheck: pass, except $missing, which could not run"
else
	echo "selfcheck: pass"
fi
exit $status
