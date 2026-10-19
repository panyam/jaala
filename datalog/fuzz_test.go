package datalog

import (
	"errors"
	"math/rand"
	"strings"
	"testing"

	"github.com/panyam/jaala/internal/corpus"
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
	`hop.reach("v1", ?x), not edge(?x, _) => ?x`,
	`out(?n, ?c) :- node(?n), ?c = count(?m) : { edge(?n, ?m) }; out(?n, ?c), ?c < 1 => ?n`,
	`node(?n), ?w = min(?v) : { weight(?n, ?v), ?v > 3 }, ?s = sum(distinct ?v) : { edge(?n, ?m), weight(?m, ?v) } => ?n, ?w, ?s`,
	`edge(?a, ?b), ?a < ?b, ?b = "v3" => ?a`,
	`r(?a, ?b) :- edge(?a, ?b); r("v0") => ?x`, // #133: a call one argument short panicked the planner
	`imports(?a,,?b) => ?a`,                         // #129: an empty piece between commas
	"r(?a) :- edge(?a, _), !edge(_, ?a).\n=> r(?a)", // #276: the endings agents write
	`?- edge(?a, ?b), ?b > 1.5.`,
	`weight(?n, ?w), ?w > 3.3V, ?w != 1eV, ?w < 2e-3kohm => ?n`,  // #167: units and exponents, which the lexer reads
	`edge(?a, ?b) as ?r, edge(?b, ?c) as ?s, ?r != ?s => ?r, ?s`, // #279: facts as terms
	`f(?r) :- edge(?a, _) as ?r, not edge(?a, ?a); f(?r), edge(_, ?b) as ?r => ?b`,
	`edge(?a, ?b) as => ?a`,
}

// fuzzVocabulary is the corpus's schema over one fixed small graph, with the path module.
func fuzzVocabulary(t testing.TB) *ns.Vocabulary {
	t.Helper()
	v := std(corpus.Graph(rand.New(rand.NewSource(7))).Source())
	if err := v.AddModule("hop", LanguageName, reachModule, ""); err != nil {
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
		facts := corpus.Graph(rnd)
		f.Add(corpus.New(rnd, facts).Program().String())
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
		verr := Validate(q, v)
		keepsPrefix(t, "Validate", text, verr)
		if pos, ok := Locate(text, verr); ok && (pos.Offset < 0 || pos.Offset >= len(text) || pos.Line < 1 || pos.Column < 1) {
			t.Fatalf("%q: Locate(%v) = %+v, outside the text", text, verr, pos)
		}
		b := baseFor(v)
		var over *BudgetExceeded
		_, err = Naive{}.Eval(bg, q, b, Budget(fuzzBudget))
		if errors.As(err, &over) {
			return
		}
		keepsPrefix(t, "Naive", text, err)
		_, diff, err := agree(q, b)
		keepsPrefix(t, "agree", text, err)
		if diff != "" {
			t.Fatalf("%q: %s", text, diff)
		}
		if _, err := (SemiNaive{}).Eval(bg, q, b); err != nil {
			keepsPrefix(t, "SemiNaive", text, err)
		}
	})
}
