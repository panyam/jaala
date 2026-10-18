package nl_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
)

// depsBase is the guide's example in miniature (internal/guide).
func depsBase(t *testing.T) (*ns.Vocabulary, *datalog.Base) { return guide.Deps(t) }

func nlCases(cs []guide.Case) []nl.Case {
	out := make([]nl.Case, len(cs))
	for i, c := range cs {
		out[i] = nl.Case{Name: c.Name, Question: c.Question, Want: c.Want}
	}
	return out
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

// evalCases are the guide's questions, which the Cypher front end's TestEval asks too.
var evalCases = nlCases(guide.Cases)

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

// Costliest ranks the evaluated tries by their work, most first and a tie in case and try order, and
// leaves out tries never evaluated. control: asked for more than there are, it returns them all.
func TestEvalRanksTheCostliestDrafts(t *testing.T) {
	_, b := depsBase(t)
	worked := func(w int64) nl.Try { return nl.Try{Report: &datalog.Report{Work: w}} }
	traces := map[string]nl.Trace{
		"a": {Tries: []nl.Try{worked(5), {Kind: nl.KindParse}, worked(9)}},
		"b": {Tries: []nl.Try{worked(9), worked(1)}},
	}
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "a", Question: "a"}, {Name: "b", Question: "b"}}, b,
		func(_ context.Context, q string) (nl.Program, nl.Trace, error) {
			return nl.Program{}, traces[q], errors.New("scored elsewhere")
		})
	var got []string
	for _, d := range rep.Costliest(3) {
		got = append(got, fmt.Sprintf("%s%d:%d", d.Case, d.N, d.Try.Report.Work))
	}
	if want := []string{"a3:9", "b1:9", "a1:5"}; !slices.Equal(got, want) {
		t.Errorf("Costliest(3) = %v, want %v", got, want)
	}
	if n := len(rep.Costliest(10)); n != 4 {
		t.Errorf("control: Costliest(10) has %d drafts, want the 4 evaluated", n)
	}
}
