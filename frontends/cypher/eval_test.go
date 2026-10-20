package cypher_test

import (
	"context"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
)

var update = flag.Bool("update", false, "rewrite testdata/eval.golden")

// drafts are the Cypher a scripted model writes for the guide's questions (guide.Cases), the ones
// frontends/nl's TestEval asks in jaala's syntax, in the order it writes them: a right query for each,
// after a misspelled label for "transitive", which the refusal's did-you-mean fixes, and for "missing",
// the property the graph doesn't have, which Compile refuses and the model writes again.
var drafts = map[string][]string{
	"what does app import directly?":            {`MATCH (:Package {name: 'app'})-[:IMPORTS]->(d) RETURN d.name`},
	"what does app depend on, directly or not?": {`MATCH (:Pakage {name: 'app'})-[:IMPORTS*]->(d) RETURN DISTINCT d.name`, `MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN DISTINCT d.name`},
	"what license does util have?":              {`MATCH (p:Package {name: 'util'}) RETURN p.license`},
	"which packages have no license stated?":    {`MATCH (p:Package) WHERE p.license IS NULL RETURN p.name`},
	"how many packages does app depend on?":     {`MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN count(DISTINCT d)`},
	"what imports util?":                        {`MATCH (x:Package)-[:IMPORTS]->(:Package {name: 'util'}) RETURN x.name`},
	"list every package with its license":       {`MATCH (p:Package) RETURN p.name, p.license`},
	"who maintains api?":                        {`MATCH (p:Package {name: 'api'}) RETURN p.maintainer`, `MATCH (p:Package {name: 'api'}) RETURN p.maintainer`},
}

// scripted answers each question with its drafts in turn, as a model reading each refusal would.
type scripted struct{}

func (scripted) Name() string { return "scripted" }

func (scripted) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	ds := drafts[p.Turns[0].Text]
	return nl.Completion{Text: ds[min(len(p.Turns)/2, len(ds)-1)]}, nil // a refused draft adds two turns
}

// translate is nl.Translate with the Cypher front end over the guide's graph, running each draft over b.
func translate(v *ns.Vocabulary, b *datalog.Base) func(context.Context, string) (nl.Program, nl.Trace, error) {
	fe := cypher.NewFrontEnd(depsGraph, nl.Example{Question: "what does api import?", Program: `MATCH (:Package {name: 'api'})-[:IMPORTS]->(d) RETURN d.name`})
	return func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
		return nl.Translate(ctx, q, v, nil, scripted{}, nl.Options{Base: b, FrontEnd: fe})
	}
}

// TestEval is mission #356's exercise: the guide's questions in Cypher, translated by nl.Translate
// with the Cypher front end (#354) and a scripted model, compiled and answered over the same Base and scored by nl.Eval against the same rows as frontends/nl's TestEval, its report
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
		t.Logf("%s: %s\n  %s", r.Name, r.Program.Text, r.Program.Compiled)
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
	if rep.Passed != 7 || rep.PassedFirstTry != 6 || rep.Language() != "cypher" {
		t.Errorf("passed %d, %d first try, in %q; want every case but missing, transitive on its second", rep.Passed, rep.PassedFirstTry, rep.Language())
	}
	if r := rep.Cases[1]; len(r.Trace.Tries) != 2 || !strings.Contains(r.Trace.Tries[0].Err.Error(), `did you mean "Package"?`) {
		t.Errorf("transitive's first try: %+v", r.Trace.Tries)
	}
	if r := rep.Cases[7]; r.Pass || r.Err == nil {
		t.Errorf("control: missing passed (%v), so a refused draft can't fail a case", r.Err)
	}
}

// turnsOf scripts a model per question, recording every prompt it was sent.
type turnsOf struct {
	drafts map[string][]string
	sent   *[]nl.Prompt
}

func (turnsOf) Name() string { return "scripted" }

func (m turnsOf) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	*m.sent = append(*m.sent, p)
	ds := m.drafts[p.Turns[0].Text]
	return nl.Completion{Text: ds[min(len(p.Turns)/2, len(ds)-1)]}, nil
}

// The translator takes a constant column on its first draft (#454), and a test returned as a value is
// refused with the WHERE rewrite in the text the model reads, so the draft after it, written in WHERE,
// passes on the second try.
func TestTheTranslatorTakesAConstantColumnAndTellsATestToMoveToWhere(t *testing.T) {
	v, b := guide.Deps(t)
	fe := cypher.NewFrontEnd(depsGraph)
	var sent []nl.Prompt
	model := turnsOf{map[string][]string{
		"is app a package?": {`MATCH (p:Package {name: 'app'}) RETURN p.name, 'yes' AS found`},
		"does log import util?": {
			`MATCH (:Package {name: 'log'})-[:IMPORTS]->(d) RETURN d.name IN ['util'] AS hit`,
			`MATCH (:Package {name: 'log'})-[:IMPORTS]->(d) WHERE d.name IN ['util'] RETURN d.name`,
		},
	}, &sent}
	_, tr, err := nl.Translate(context.Background(), "is app a package?", v, nil, model, nl.Options{Base: b, FrontEnd: fe})
	if err != nil || len(tr.Tries) != 1 || tr.Tries[0].Err != nil {
		t.Errorf("a constant column: %v, %d tries, first refused as %v; want it taken on the first", err, len(tr.Tries), tr.Tries[0].Err)
	}
	sent = nil
	_, tr, err = nl.Translate(context.Background(), "does log import util?", v, nil, model, nl.Options{Base: b, FrontEnd: fe})
	if err != nil || len(tr.Tries) != 2 || tr.Tries[0].Kind != "compile" || tr.Tries[1].Err != nil {
		t.Fatalf("IN returned as a value: %v, %d tries; want a compile refusal and then an answer", err, len(tr.Tries))
	}
	last := sent[len(sent)-1].Turns
	if read := last[len(last)-1].Text; !strings.Contains(read, "test it in WHERE") {
		t.Errorf("the model read %q after its first draft, which doesn't say to move IN into WHERE", read)
	}
}
