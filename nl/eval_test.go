package nl_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// depsBase is the guide's example in miniature: app imports api, api imports log, log imports util,
// four packages' licenses (api's absent, log's empty), and the deps module reaching through imports.
func depsBase(t *testing.T) (*ns.Vocabulary, *datalog.Base) {
	t.Helper()
	src := ns.NewMemSource().
		DeclareSchema("imports", ns.Schema{Arity: 2, Labels: []string{"from", "to"}, Doc: "A package importing another."}).
		DeclareSchema("pkg", ns.Schema{Arity: 2, Labels: []string{"name", "license"}, Doc: "Every package and its license."})
	for _, e := range [][2]string{{"app", "api"}, {"api", "log"}, {"log", "util"}} {
		src.Add("imports", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1])}})
	}
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("app"), ns.S("MIT")}})
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("api"), ns.Absent()}})
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("log"), ns.S("")}})
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("util"), ns.S("Apache-2.0")}})
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	if err := v.AddModule("deps", datalog.LanguageName, `# Everything a package depends on, directly or through others.
depends_on(?a, ?b) :- _step(?a, ?b);
depends_on(?a, ?c) :- depends_on(?a, ?b), _step(?b, ?c);
_step(?a, ?b) :- imports(?a, ?b);
`, ""); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return v, b
}

// byQuestion is a Model answering each question with its own drafts in turn, as a model that makes
// the same mistakes every run would. Its tokens are the prompt's and the reply's length over four.
type byQuestion struct {
	name   string
	drafts map[string][]string
	asked  map[string]int
}

func (m *byQuestion) Name() string { return m.name }

func (m *byQuestion) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	q, _, _ := strings.Cut(p.Turns[0].Text, "\n")
	ds := m.drafts[q]
	n := m.asked[q]
	m.asked[q]++
	if n >= len(ds) {
		return nl.Completion{}, errors.New("no draft left")
	}
	in := len(p.Prefix)
	for _, t := range p.Turns {
		in += len(t.Text)
	}
	return nl.Completion{Text: ds[n], TokensIn: in / 4, TokensCached: len(p.Prefix) / 4, TokensOut: len(ds[n]) / 4}, nil
}

func drafter(name string, drafts map[string][]string) *byQuestion {
	return &byQuestion{name: name, drafts: drafts, asked: map[string]int{}}
}

// evalCases are questions as the guide asks them, with the rows a right program answers.
var evalCases = []nl.Case{
	{Name: "direct", Question: "what does app import directly?", Want: [][]string{{"api"}}},
	{Name: "transitive", Question: "what does app depend on, directly or not?", Want: [][]string{{"api"}, {"log"}, {"util"}}},
	{Name: "license", Question: "what license does util have?", Want: [][]string{{"Apache-2.0"}}},
	{Name: "unstated", Question: "which packages have no license stated?", Want: [][]string{{"api"}}},
	{Name: "count", Question: "how many packages does app depend on?", Want: [][]string{{"3"}}},
	{Name: "reverse", Question: "what imports util?", Want: [][]string{{"log"}}},
	{Name: "columns", Question: "list every package with its license", Want: [][]string{{"app", "MIT"}, {"api", "absent"}, {"log", ""}, {"util", "Apache-2.0"}}},
	{Name: "missing", Question: "who maintains api?", Want: [][]string{{"someone"}}},
}

// The small model's drafts: right first, a missing module, a wrong arity, an unbound variable, an
// uppercase constant, a program answering nothing, columns in another order, and a relation the
// vocabulary doesn't hold. The fallback gets the ones the small model gives up on.
var smallDrafts = map[string][]string{
	"what does app import directly?":            {`imports("app", ?d) => ?d`},
	"what does app depend on, directly or not?": {`depends_on("app", ?d) => ?d`, `deps.depends_on("app", ?d) => ?d`},
	"what license does util have?":              {`pkg("util") => ?l`, `pkg("util", ?l) => ?l`},
	"which packages have no license stated?":    {`pkg(?p, ?l), absent(?x) => ?p`, `pkg(?p, ?l), absent(?l) => ?p`},
	"how many packages does app depend on?":     {`deps.depends_on(App, ?d) => count(?d)`, `deps.depends_on("app", ?d) => count(?d)`},
	"what imports util?":                        {`imports("util", ?x) => ?x`},
	"list every package with its license":       {`pkg(?n, ?l) => ?l, ?n`},
	"who maintains api?":                        {`maintainer("api", ?m) => ?m`, `maintainer("api", ?m) => ?m`, `owner("api", ?m) => ?m`},
}

var bigDrafts = map[string][]string{
	"what imports util?": {`imports(?x, "util") => ?x`},
	"who maintains api?": {`pkg("api", ?m) => ?m`},
}

// TestEval is mission #322's exercise until Declaire's translator-alone arm runs: Eval over the
// guide's questions with a scripted small model and fallback, its report pinned in testdata/eval.golden
// (-update rewrites it). -v prints the card's size and the report.
func TestEval(t *testing.T) {
	v, b := depsBase(t)
	card, err := nl.Card(v)
	if err != nil {
		t.Fatal(err)
	}
	small, big := drafter("small", smallDrafts), drafter("big", bigDrafts)
	rep := nl.Eval(context.Background(), evalCases, b, func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
		return nl.Translate(ctx, q, v, nil, small, nl.Options{Base: b, Fallback: big})
	})
	got := rep.String()
	t.Logf("card: %d bytes, %d lines\n%s", len(card), strings.Count(card, "\n"), got)
	const golden = "testdata/eval.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("report:\n%s\nwant:\n%s", got, want)
	}
	if rep.Passed != 7 || rep.PassedFirstTry != 2 || rep.Checked == 0 || rep.Fixed == 0 {
		t.Errorf("passed %d, %d first try, %d of %d check errors with a fix", rep.Passed, rep.PassedFirstTry, rep.Fixed, rep.Checked)
	}
	if r := rep.Cases[7]; r.Pass || r.Err != nil || r.Program.Model != "big" || len(r.Got) != 1 || r.Got[0][0] != "absent" {
		t.Errorf("missing: pass %v, %v, by %s, got %v; want the fallback's wrong answer", r.Pass, r.Err, r.Program.Model, r.Got)
	}
}

// A row matches by its values whatever its columns' order, rows as a multiset unless Ordered, and a
// duplicate row counts.
func TestEvalMatchesRowsByTheirValues(t *testing.T) {
	v, b := depsBase(t)
	for _, c := range []struct {
		draft   string
		want    [][]string
		ordered bool
		pass    bool
	}{
		{`imports(?a, ?b) => ?b, ?a`, [][]string{{"api", "app"}, {"log", "api"}, {"util", "log"}}, false, true},
		{`imports(?a, ?b) => ?a, ?b`, [][]string{{"log", "util"}, {"app", "api"}, {"api", "log"}}, false, true},
		{`imports(?a, ?b) => ?a, ?b order by ?a`, [][]string{{"log", "util"}, {"app", "api"}, {"api", "log"}}, true, false},
		{`imports(?a, ?b) => ?a, ?b order by ?a`, [][]string{{"api", "log"}, {"app", "api"}, {"log", "util"}}, true, true},
		{`imports(?a, ?b) => ?a`, [][]string{{"app"}, {"api"}}, false, false},
		{`imports(?a, ?b) => ?a`, [][]string{{"app"}, {"api"}, {"log"}, {"log"}}, false, false},
	} {
		m := drafter("small", map[string][]string{"q": {c.draft}})
		rep := nl.Eval(context.Background(), []nl.Case{{Name: "q", Question: "q", Want: c.want, Ordered: c.ordered}}, b, func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return nl.Translate(ctx, q, v, nil, m, nl.Options{})
		})
		if rep.Cases[0].Pass != c.pass {
			t.Errorf("%s against %v (ordered %v): pass %v, got %v", c.draft, c.want, c.ordered, rep.Cases[0].Pass, rep.Cases[0].Got)
		}
	}
}

// Rows Translate already ran over a Base are scored as they are, not evaluated again, and a case whose
// translate fails fails alone.
func TestEvalScoresEachCaseOnItsOwn(t *testing.T) {
	_, b := depsBase(t)
	ran := nl.Program{Rows: []datalog.Row{{Bind: map[datalog.Var]ns.Value{"x": ns.S("kept")}}}} // its zero Query wouldn't evaluate
	rep := nl.Eval(context.Background(), []nl.Case{
		{Name: "fails", Question: "a", Want: [][]string{{"x"}}},
		{Name: "ran", Question: "b", Want: [][]string{{"kept"}}},
	}, b, func(_ context.Context, q string) (nl.Program, nl.Trace, error) {
		if q == "a" {
			return nl.Program{}, nl.Trace{Tries: []nl.Try{{Kind: nl.KindModel, Err: errors.New("down")}}}, errors.New("down")
		}
		return ran, nl.Trace{Tries: []nl.Try{{TokensIn: 7}}}, nil
	})
	if len(rep.Cases) != 2 || rep.Cases[0].Pass || rep.Cases[0].Err == nil || !rep.Cases[1].Pass || rep.Cases[1].Err != nil {
		t.Fatalf("cases %+v", rep.Cases)
	}
	if rep.Passed != 1 || rep.PassedFirstTry != 1 || rep.Tries != 2 || rep.TokensIn != 7 || rep.Refused[nl.KindModel] != 1 {
		t.Errorf("report %+v", rep)
	}
}
