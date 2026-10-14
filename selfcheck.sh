#!/bin/sh
# selfcheck runs jaala's checks that need no host: every evaluator against Naive on generated
# programs (#87), work against baselines (#88), fuzzed query text (#89), and answers against Soufflé
# (#23), when souffle is on PATH. See #86. JAALA_GEN_SEEDS overrides the seed count, for the corpus and
# for Soufflé, and JAALA_FUZZ_TIME each target's fuzzing time.
set -u
cd "$(dirname "$0")"
seeds=${JAALA_GEN_SEEDS:-5000}
fuzztime=${JAALA_FUZZ_TIME:-30s}
out=$(mktemp)
trap 'rm -f "$out"' EXIT
status=0

echo "== generated programs: $seeds seeds"
JAALA_GEN_SEEDS=$seeds go test -count=1 ./datalog -run '^(TestGeneratedProgramsAgree|TestBrokenProgramsFailTheSameWay)$' -v >"$out" 2>&1 || status=1
# Relation names a rewrite adds carry a NUL, which would make grep treat the log as binary.
tr -d '\000' <"$out" | grep -E 'seeds$|compared|skipped|known|broken programs|written:|disagrees|refuses|replay|shrunk|facts:|bind:|naive:|planned:|seminaive:|^(--- |ok|FAIL)'

echo "== work against baselines"
go test -count=1 ./datalog -run '^TestWorkStaysWithinBaseline$' -v >"$out" 2>&1 || status=1
grep -E 'work |over its|under its|has no baseline|no workload|^(--- |ok|FAIL)' "$out" | sed 's/^ *bench_test.go:[0-9]*: /  /'

echo "== fuzzed query text: $fuzztime per target"
fuzz() { go test -count=1 ./datalog -run '^$' -fuzz "^$1\$" -fuzztime "$fuzztime" >"$out" 2>&1; }
# saved is the input a failed run wrote, if any; it is a finding only if it still fails replayed.
saved() { tr -d '\000' <"$out" | sed -n 's|.*Failing input written to testdata/fuzz/[^/]*/\([0-9a-f]*\).*|\1|p' | head -n 1; }
replays() { go test -count=1 ./datalog -run "^$1/$2\$" >/dev/null 2>&1; }
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
		rm -f "datalog/testdata/fuzz/$1/$id"
		echo "  $1: the fuzz engine failed; the input it saved ($id) passes replayed, so it was removed. Its last lines:"
	else
		echo "  $1: the fuzz engine failed without saving an input. Its last lines:"
	fi
	tail30
	return 2
}
for target in FuzzParse FuzzEval; do
	# go test fuzzes one target at a time. A failing input is written to datalog/testdata/fuzz/<target>,
	# which plain go test then replays: fix it, and commit the file as a seed. An engine failure gets
	# one rerun, so it reads differently from a finding.
	fuzzed "$target"
	case $? in
	0) echo "  $target: no failing input in $fuzztime" ;;
	1) status=1 ;;
	2) if fuzzed "$target"; then
		echo "  $target: no failing input in $fuzztime on a rerun, so the first run's failure was the engine's"
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

if [ $status -ne 0 ]; then
	echo "selfcheck: FAIL"
elif [ $souffle_ran = no ]; then
	echo "selfcheck: pass, except Soufflé, which could not run"
else
	echo "selfcheck: pass"
fi
exit $status
