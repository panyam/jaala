package datalog

import (
	"errors"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// Fuzzing the query text (#89). The generated corpus writes only well-formed programs and breaks them
// three ways; these targets take whatever bytes the fuzzer makes up. Plain go test runs every seed below
// and every input in testdata/fuzz, so CI replays them; ./selfcheck.sh fuzzes each target for a while
// (JAALA_FUZZ_TIME), and an input it finds failing is kept in testdata/fuzz once fixed.

// fuzzSeeds are programs over the corpus's vocabulary (edge, node, weight, and the path module), one
// per feature, plus the inputs fuzzing or hosts have found trouble with.
var fuzzSeeds = []string{
	`edge(?a, ?b) => ?a, ?b`,
	`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); reach("v0", ?x) => ?x`,
	`has(?n) :- edge(_, ?n); node(?n), not has(?n) => ?n`,
	`deg(?n, count(?m)) :- edge(?n, ?m); deg(?n, ?c), ?c > 0 => ?n, ?c order by ?c desc, ?n limit 2 offset 1`,
	`node(?n), str.contains(?n, "v") => count(distinct ?n)`,
	`weight(?n, ?w), ?w >= 2 => ?n, sum(?w), max(?w)`,
	`t(?n: node, ?w: number) :- weight(?n, ?w); t(?n, ?w), ?w != 3 => list(?n)`,
	`path.reach("v1", ?x), not edge(?x, _) => ?x`,
	`edge(?a, ?b), ?a < ?b, ?b = "v3" => ?a`,
	`r(?a, ?b) :- edge(?a, ?b); r("v0") => ?x`, // #133: a call one argument short panicked the planner
	`imports(?a,,?b) => ?a`, // #129: an empty piece between commas
}

// fuzzVocabulary is the corpus's schema over one fixed small graph, with the path module.
func fuzzVocabulary(t testing.TB) *ns.Vocabulary {
	t.Helper()
	v := std(genGraph(rand.New(rand.NewSource(7))).source())
	if err := v.AddModule("path", LanguageName, reachModule, ""); err != nil {
		t.Fatal(err)
	}
	return v
}

// addSeeds adds the hand-picked seeds and a hundred of the corpus generator's programs.
func addSeeds(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	for seed := int64(1); seed <= 100; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		facts := genGraph(rnd)
		f.Add((&generator{rnd: rnd, nodes: facts.n}).program().String())
	}
}

// keepsPrefix fails t unless err is nil or reads as one of the engine's errors.
func keepsPrefix(t *testing.T, where, text string, err error) {
	t.Helper()
	if err != nil && !strings.HasPrefix(err.Error(), "query:") {
		t.Fatalf("%s: error without its query: prefix on %q: %v", where, text, err)
	}
}

// Any text parses or is refused with a query: error, never a panic. What parses prints back as text
// that parses to the same rules, which Rule.String promises.
func FuzzParse(f *testing.F) {
	addSeeds(f)
	f.Fuzz(func(t *testing.T, text string) {
		q, err := Parse(text)
		keepsPrefix(t, "Parse", text, err)
		if err != nil {
			return
		}
		for _, r := range q.Rules {
			again, err := ParseRules(r.String())
			if err != nil || len(again) != 1 || again[0].String() != r.String() {
				t.Fatalf("rule %q from %q prints as %q, which parses to %v, %v", r, text, r.String(), again, err)
			}
		}
	})
}

// fuzzBudget bounds Naive's work on a fuzzed program, as genBudget does the corpus's, but lower: a
// fuzzer runs many inputs a second, and a dense recursion over the small graph is still within it.
const fuzzBudget = 20_000

// Any program that parses is checked and evaluated without a panic, and every error keeps its query:
// prefix. One Naive answers within the budget must get the same answer from every evaluator (agree),
// which extends the corpus's comparison to text the generator never writes.
func FuzzEval(f *testing.F) {
	addSeeds(f)
	v := fuzzVocabulary(f)
	f.Fuzz(func(t *testing.T, text string) {
		q, err := Parse(text)
		if err != nil {
			return
		}
		keepsPrefix(t, "Validate", text, Validate(q, v))
		b := baseFor(v)
		var over *BudgetExceeded
		_, err = Naive{}.Eval(bg, q, b, Budget(fuzzBudget))
		if errors.As(err, &over) {
			return
		}
		keepsPrefix(t, "Naive", text, err)
		_, diff, err := agree(q, b)
		keepsPrefix(t, "agree", text, err)
		if diff != "" && spelledApart(q, b) {
			t.Skipf("known disagreement #164 (two facts spelling one number differently): %q", text)
		}
		if diff != "" {
			t.Fatalf("%q: %s", text, diff)
		}
		if _, err := (SemiNaive{}).Eval(bg, q, b); err != nil {
			keepsPrefix(t, "SemiNaive", text, err)
		}
	})
}

// spelledApart reports whether every evaluator answers q with the same rows once each number is written
// canonically, so a difference between them is only which spelling of a number reached the answer:
// #164, counted rather than failed until it is fixed. A spelling the query wrote gives way to data's
// (#148), so what this still counts is data spelling one number two ways.
func spelledApart(q Query, b *Base) bool {
	var want []string
	for i, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		rows, err := ev.Eval(bg, q, b)
		if err != nil {
			return false
		}
		var got []string
		for _, r := range rows {
			got = append(got, canonicalRow(r))
		}
		sort.Strings(got)
		if i == 0 {
			want = got
		} else if !slices.Equal(got, want) {
			return false
		}
	}
	return true
}

// canonicalRow is a row's bindings with each number written as ftoa writes it.
func canonicalRow(r Row) string {
	keys := make([]string, 0, len(r.Bind))
	for v, val := range r.Bind {
		text := val.S
		if val.Num != nil {
			text = ftoa(*val.Num)
		}
		keys = append(keys, string(v)+"="+text)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// #164, pinned: the head spells zero 0 and 00, and the goal reads both into ?0. Naive solves the goal
// as written and answers 0; the planned SemiNaive runs the second literal first and answers 00.
// FuzzEval counts this case rather than failing on it, so once #164 is fixed this test fails, and it
// goes, with the skip.
func TestTwoFactsSpellingOneNumberAreAKnownDisagreement(t *testing.T) {
	v := fuzzVocabulary(t)
	q := mustParse(t, `r0(0, 0, 00) :- weight(?00, ?0); r0(0, ?0, 0), r0("0", 0, ?0)`)
	b := baseFor(v)
	_, diff, err := agree(q, b)
	if err != nil || diff == "" {
		t.Fatalf("#164 no longer disagrees (%v): drop this test and the skip in FuzzEval", err)
	}
	if !spelledApart(q, b) {
		t.Errorf("the repro disagrees in more than spelling:\n%s", diff)
	}
}
