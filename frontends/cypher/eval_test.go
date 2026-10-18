package cypher_test

import (
	"context"
	"flag"
	"os"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
)

var update = flag.Bool("update", false, "rewrite testdata/eval.golden")

// drafts are the Cypher a model writes for the guide's questions (guide.Cases), the ones frontends/nl's
// TestEval asks in jaala's syntax: a right query for each, and for "missing", the property the graph
// doesn't have, which Compile refuses.
var drafts = map[string]string{
	"what does app import directly?":            `MATCH (:Package {name: 'app'})-[:IMPORTS]->(d) RETURN d.name`,
	"what does app depend on, directly or not?": `MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN DISTINCT d.name`,
	"what license does util have?":              `MATCH (p:Package {name: 'util'}) RETURN p.license`,
	"which packages have no license stated?":    `MATCH (p:Package) WHERE p.license IS NULL RETURN p.name`,
	"how many packages does app depend on?":     `MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN count(DISTINCT d)`,
	"what imports util?":                        `MATCH (x:Package)-[:IMPORTS]->(:Package {name: 'util'}) RETURN x.name`,
	"list every package with its license":       `MATCH (p:Package) RETURN p.name, p.license`,
	"who maintains api?":                        `MATCH (p:Package {name: 'api'}) RETURN p.maintainer`,
}

// translate compiles the case's draft and evaluates it, the way a Cypher Translate (#354) will once a
// model writes the drafts: a refused draft is one try that failed.
func translate(v *ns.Vocabulary, b *datalog.Base) func(context.Context, string) (nl.Program, nl.Trace, error) {
	return func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
		try := nl.Try{Model: "scripted", Text: drafts[q]}
		fail := func(kind string, err error) (nl.Program, nl.Trace, error) {
			try.Kind, try.Err = kind, err
			return nl.Program{}, nl.Trace{Tries: []nl.Try{try}}, err
		}
		p, err := cypher.Compile(v, depsGraph, try.Text)
		if ce, ok := err.(*cypher.Error); ok && ce.Gap != 0 {
			return fail("gap", err)
		} else if err != nil {
			return fail("compile", err)
		}
		if err := datalog.Validate(p.Query, v); err != nil {
			return fail(nl.KindEval, err)
		}
		try.Report = &datalog.Report{}
		rows, err := datalog.SemiNaive{}.Eval(ctx, p.Query, b, datalog.Explain(try.Report))
		if err != nil {
			return fail(nl.KindEval, err)
		}
		return nl.Program{Text: p.Text, Query: p.Query, Rows: rows, Model: try.Model}, nl.Trace{Tries: []nl.Try{try}}, nil
	}
}

// TestEval is mission #356's exercise: the guide's questions in Cypher, compiled and answered over
// the same Base and scored by nl.Eval against the same rows as frontends/nl's TestEval, its report
// pinned in testdata/eval.golden (-update rewrites it). -v prints the report and each program.
func TestEval(t *testing.T) {
	v, b := guide.Deps(t)
	cases := make([]nl.Case, len(guide.Cases))
	for i, c := range guide.Cases {
		cases[i] = nl.Case{Name: c.Name, Question: c.Question, Want: c.Want}
	}
	rep := nl.Eval(context.Background(), cases, b, translate(v, b))
	got := rep.String()
	for _, r := range rep.Cases {
		t.Logf("%s: %s\n  %s", r.Name, drafts[r.Question], r.Program.Text)
	}
	t.Logf("\n%s", got)
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
	if rep.Passed != 7 || rep.PassedFirstTry != 7 {
		t.Errorf("passed %d, %d first try; want every case but missing", rep.Passed, rep.PassedFirstTry)
	}
	if r := rep.Cases[7]; r.Pass || r.Err == nil {
		t.Errorf("control: missing passed (%v), so a refused draft can't fail a case", r.Err)
	}
}
